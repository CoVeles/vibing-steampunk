package adt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Upload transport: a released request's two files, the cofile K<nr>.<SID>
// and the data file R<nr>.<SID>, are written into DIR_TRANS of the connected
// system (cofiles/ and data/), and the request is added to that system's
// import buffer. Nothing is imported: the import stays a human step in STMS.
//
// The ABAP side is ZCL_VSP_TRANSPORT_SERVICE (domain "transport"). It writes
// only through SAP's EPS file layer with the logical directories $TR_COFI and
// $TR_DATA, never overwrites a file that exists, and sends tp exactly one
// command, the literal ADDTOBUFFER, for sy-sysid. Every check made here is
// made again there.

const (
	// TransportUploadMaxBytes caps the two files together.
	TransportUploadMaxBytes = 50 << 20
	// TransportCofileMaxBytes caps the cofile. A cofile is a few lines per
	// export and import step; one this large is not a cofile.
	TransportCofileMaxBytes = 1 << 20
	// transportUploadChunk is the payload of one WebSocket message, before
	// base64. ZADT_VSP parses every message character by character, so a
	// moderate chunk keeps each message well under a second there.
	transportUploadChunk = 256 << 10
	// transportDownloadChunk is what one download message asks for.
	transportDownloadChunk = 512 << 10
)

var (
	cofileNameRe = regexp.MustCompile(`^K([0-9]{6})\.([A-Z0-9]{3})$`)
	dataNameRe   = regexp.MustCompile(`^R([0-9]{6})\.([A-Z0-9]{3})$`)
	requestRe    = regexp.MustCompile(`^([A-Z0-9]{3})K([0-9]{6})$`)

	cofileTargetRe = regexp.MustCompile(`^[A-Z0-9/_]{1,20}(\.[0-9]{3})?$`)
	digitsRe       = regexp.MustCompile(`^[0-9]+$`)
	timestampRe    = regexp.MustCompile(`^[0-9]{14}$`)
	upperLetterRe  = regexp.MustCompile(`^[A-Z]$`)
	cofileStepRe   = regexp.MustCompile(`^[0-3]$`)
)

// TransportFiles is one request's cofile and data file, validated.
type TransportFiles struct {
	// Request is <SID>K<number>, as the file names give it.
	Request    string
	SID        string
	Number     string
	CofileName string
	DataName   string
	Cofile     []byte
	Data       []byte
}

// TransportRequestFromFileNames checks a cofile and a data file name and
// returns the request they belong to. The names are bare file names: no
// directory, no path separator. Both are required, and the number and SID of
// the two must be the same.
func TransportRequestFromFileNames(cofileName, dataName string) (request, sid, number string, err error) {
	if cofileName == "" || dataName == "" {
		return "", "", "", errors.New("both files are required: the cofile K<nr>.<SID> and the data file R<nr>.<SID>")
	}
	c := cofileNameRe.FindStringSubmatch(cofileName)
	if c == nil {
		return "", "", "", fmt.Errorf("cofile name %q is not K<6 digits>.<SID> (e.g. K900123.DEV)", cofileName)
	}
	d := dataNameRe.FindStringSubmatch(dataName)
	if d == nil {
		return "", "", "", fmt.Errorf("data file name %q is not R<6 digits>.<SID> (e.g. R900123.DEV)", dataName)
	}
	if c[1] != d[1] || c[2] != d[2] {
		return "", "", "", fmt.Errorf("%s and %s are not one request's files: number and SID must match", cofileName, dataName)
	}
	return c[2] + "K" + c[1], c[2], c[1], nil
}

// TransportFileNamesForRequest is the cofile and data file name of a request.
func TransportFileNamesForRequest(request string) (cofileName, dataName string, err error) {
	m := requestRe.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(request)))
	if m == nil {
		return "", "", fmt.Errorf("request %q is not <SID>K<6 digits>", request)
	}
	return "K" + m[2] + "." + m[1], "R" + m[2] + "." + m[1], nil
}

// ValidateCofile checks that content has the shape of a cofile of a request
// exported from sid, the way STRF_READ_COFILE reads one: lines starting with
// '#' are directives; the first other non-blank line is the header
// "truser trfunction tarsystem step objcount..."; every line after it is a
// step "<system>[.<client>] <function> <retcode> <YYYYMMDDhhmmss> <host> <osuser>".
// An export step of sid must be among them -- a request that was never
// exported from sid has no data file of sid to go with it.
func ValidateCofile(content []byte, sid string) error {
	if len(content) == 0 {
		return errors.New("the cofile is empty")
	}
	if len(content) > TransportCofileMaxBytes {
		return fmt.Errorf("the cofile is %d bytes, over the %d-byte limit for a cofile", len(content), TransportCofileMaxBytes)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return errors.New("the cofile contains NUL bytes: it is not a cofile (is it the data file?)")
	}
	if !utf8.Valid(content) {
		return errors.New("the cofile is not text")
	}
	for _, r := range string(content) {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("the cofile contains control character 0x%02x: it is not a cofile", r)
		}
	}
	header, steps, export := false, 0, false
	for i, line := range strings.Split(string(content), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if !header {
			if err := validateCofileHeader(f); err != nil {
				return fmt.Errorf("cofile line %d (the header): %w", i+1, err)
			}
			header = true
			continue
		}
		if len(f) < 4 {
			return fmt.Errorf("cofile line %d: a step line has at least system, function, return code and time", i+1)
		}
		if !cofileTargetRe.MatchString(f[0]) || len(f[1]) != 1 || !digitsRe.MatchString(f[2]) || !timestampRe.MatchString(f[3]) {
			return fmt.Errorf("cofile line %d is not a step line (<system>[.<client>] <function> <retcode> <YYYYMMDDhhmmss> ...)", i+1)
		}
		steps++
		if strings.SplitN(f[0], ".", 2)[0] == sid && f[1] == "E" {
			export = true
		}
	}
	if !header {
		return errors.New("the cofile has no header line")
	}
	if steps == 0 {
		return errors.New("the cofile records no steps: the request was never exported")
	}
	if !export {
		return fmt.Errorf("the cofile records no export (step E) from %s, the system its name says it comes from", sid)
	}
	return nil
}

func validateCofileHeader(f []string) error {
	if len(f) < 4 {
		return errors.New("a header has at least owner, request type, target and step")
	}
	if !upperLetterRe.MatchString(f[1]) {
		return fmt.Errorf("request type %q is not a single letter", f[1])
	}
	if !cofileTargetRe.MatchString(f[2]) {
		return fmt.Errorf("target %q is not a system", f[2])
	}
	if !cofileStepRe.MatchString(f[3]) {
		return fmt.Errorf("step %q is not one of 0, 1, 2, 3", f[3])
	}
	// Nine object counts follow the step, which is what STRF_READ_COFILE
	// reads; a real header goes on (release, flags, client) and that is not
	// checked.
	for i, n := range f[4:] {
		if i == 9 {
			break
		}
		if !digitsRe.MatchString(n) {
			return fmt.Errorf("object count %q is not a number", n)
		}
	}
	return nil
}

// NewTransportFiles validates a request's two files: the names, the pairing,
// the sizes and the cofile's shape. It does no I/O.
func NewTransportFiles(cofileName string, cofile []byte, dataName string, data []byte) (*TransportFiles, error) {
	request, sid, number, err := TransportRequestFromFileNames(cofileName, dataName)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("the data file %s is empty", dataName)
	}
	if total := len(cofile) + len(data); total > TransportUploadMaxBytes {
		return nil, fmt.Errorf("the two files are %d bytes together, over the %d-byte (50 MB) limit", total, TransportUploadMaxBytes)
	}
	if err := ValidateCofile(cofile, sid); err != nil {
		return nil, fmt.Errorf("%s: %w", cofileName, err)
	}
	return &TransportFiles{
		Request: request, SID: sid, Number: number,
		CofileName: cofileName, DataName: dataName,
		Cofile: cofile, Data: data,
	}, nil
}

// ReadTransportFiles reads a request's two files from local paths. Each must
// be a regular file, not a symbolic link, within the size limits; the names
// are the paths' base names.
func ReadTransportFiles(cofilePath, dataPath string) (*TransportFiles, error) {
	if cofilePath == "" || dataPath == "" {
		return nil, errors.New("both files are required: the cofile K<nr>.<SID> and the data file R<nr>.<SID>")
	}
	cofileName, dataName := filepath.Base(cofilePath), filepath.Base(dataPath)
	if _, _, _, err := TransportRequestFromFileNames(cofileName, dataName); err != nil {
		return nil, err
	}
	cofile, err := readRegularFile(cofilePath, TransportCofileMaxBytes)
	if err != nil {
		return nil, err
	}
	data, err := readRegularFile(dataPath, TransportUploadMaxBytes)
	if err != nil {
		return nil, err
	}
	return NewTransportFiles(cofileName, cofile, dataName, data)
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link; name the file itself", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, fi.Size(), limit)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, len(b), limit)
	}
	return b, nil
}

// --- gates -----------------------------------------------------------------

// CheckTransportUpload runs every policy check an upload of request needs,
// before anything is read or sent: not read-only (stated outright, because
// the read-only operation filter does not cover transport operations),
// transports enabled, transports not read-only, and request inside the
// transport whitelist. With an empty request it checks everything but the
// whitelist, so a caller can refuse before it has even looked at the files.
// It does no I/O.
func (c *Client) CheckTransportUpload(request string) error {
	const op = "UploadTransport"
	request = strings.ToUpper(strings.TrimSpace(request))
	if c.config.Safety.ReadOnly {
		return fmt.Errorf("transport write operation '%s' is blocked: read-only mode enabled", op)
	}
	if err := c.config.Safety.CheckTransport(request, op, true); err != nil {
		return err
	}
	if err := c.checkSafety(OpTransport, op); err != nil {
		return err
	}
	if request != "" && !requestRe.MatchString(request) {
		return fmt.Errorf("request %q is not <SID>K<6 digits>", request)
	}
	return nil
}

// CheckTransportBufferRead runs the checks a read of the import buffer, or of
// a request's files, needs: transports enabled (or transportable edits
// allowed, as for every transport read), and request -- when one is named --
// inside the transport whitelist. It does no I/O.
func (c *Client) CheckTransportBufferRead(request, op string) error {
	request = strings.ToUpper(strings.TrimSpace(request))
	if err := c.config.Safety.CheckTransport(request, op, false); err != nil {
		return err
	}
	if request != "" && !requestRe.MatchString(request) {
		return fmt.Errorf("request %q is not <SID>K<6 digits>", request)
	}
	return nil
}

// CheckTransportDownload runs the checks a download of a request's files
// needs. A data file can carry table contents, so this is a sensitive read:
// refused under read-only, and only with transports enabled; the whitelist
// applies. It does no I/O.
func (c *Client) CheckTransportDownload(request string) error {
	const op = "DownloadTransportFiles"
	request = strings.ToUpper(strings.TrimSpace(request))
	if c.config.Safety.ReadOnly {
		return fmt.Errorf("operation '%s' is blocked: read-only mode enabled (a data file can carry table contents)", op)
	}
	return c.CheckTransportBufferRead(request, op)
}

// --- the ZADT_VSP transport domain -----------------------------------------

// TransportService is the part of ZADT_VSP's WebSocket client the transport
// domain needs. *DebugWebSocketClient and *AMDPWebSocketClient have it.
type TransportService interface {
	SendDomainRequest(ctx context.Context, domain, action string, params map[string]any, timeout time.Duration) (*WSResponse, error)
}

const transportDomain = "transport"

// TransportUploadResult is what an upload did.
type TransportUploadResult struct {
	Request    string `json:"request"`
	System     string `json:"system"`
	Client     string `json:"client"`
	CofileName string `json:"cofile"`
	DataName   string `json:"datafile"`
	CofileSize int    `json:"cofileSize"`
	DataSize   int    `json:"dataSize"`
	// FilesWritten says both files are in DIR_TRANS now.
	FilesWritten bool   `json:"filesWritten"`
	CofilePath   string `json:"cofilePath,omitempty"`
	DataPath     string `json:"dataPath,omitempty"`
	// Queued says the request was added to the import buffer of System.
	Queued bool `json:"queued"`
	// RolledBack says the files written by this upload were deleted again
	// because the request could not be added to the buffer.
	RolledBack bool               `json:"rolledBack,omitempty"`
	TP         *TransportTPResult `json:"tp,omitempty"`
	// InBuffer is the buffer entry read back after the add.
	InBuffer *TransportBufferEntry `json:"inBuffer,omitempty"`
	Note     string                `json:"note"`
}

// TransportTPResult is what tp answered.
type TransportTPResult struct {
	Command    string   `json:"command,omitempty"`
	ReturnCode string   `json:"returnCode,omitempty"`
	Message    string   `json:"message,omitempty"`
	Stdout     []string `json:"stdout,omitempty"`
}

// TransportBufferEntry is one line of an import buffer.
type TransportBufferEntry struct {
	Request    string `json:"request"`
	Client     string `json:"client,omitempty"`
	SourceCli  string `json:"sourceClient,omitempty"`
	Function   string `json:"function,omitempty"`
	Owner      string `json:"owner,omitempty"`
	UModes     string `json:"umodes,omitempty"`
	ReturnCode string `json:"returnCode,omitempty"`
	Step       string `json:"step,omitempty"`
	ImpFlag    string `json:"impflg,omitempty"`
	// Raw is the buffer file's line for the request.
	Raw string `json:"raw,omitempty"`
}

// TransportBufferResult is a read of the connected system's import buffer.
type TransportBufferResult struct {
	System string `json:"system"`
	Client string `json:"client"`
	// Source is the file read, DIR_TRANS/buffer/<SID>; FileExists is false
	// when there is none (nothing was ever queued for the system).
	Source     string                 `json:"source,omitempty"`
	FileExists bool                   `json:"fileExists"`
	Request    string                 `json:"request,omitempty"`
	Total      int                    `json:"total"`
	Entries    []TransportBufferEntry `json:"entries"`
	// Truncated says there were more entries than were returned.
	Truncated bool               `json:"truncated,omitempty"`
	TP        *TransportTPResult `json:"tp,omitempty"`
}

// Contains reports whether request is in the buffer read.
func (b *TransportBufferResult) Contains(request string) (*TransportBufferEntry, bool) {
	for i := range b.Entries {
		if strings.EqualFold(b.Entries[i].Request, request) {
			return &b.Entries[i], true
		}
	}
	return nil, false
}

func transportCall(ctx context.Context, ws TransportService, action string, params map[string]any, timeout time.Duration, out any) error {
	if ws == nil {
		return errors.New("the transport upload needs ZADT_VSP (a WebSocket to the system)")
	}
	resp, err := ws.SendDomainRequest(ctx, transportDomain, action, params, timeout)
	if err != nil {
		return fmt.Errorf("transport.%s: %w", action, err)
	}
	if !resp.Success {
		if resp.Error != nil {
			if resp.Error.Code == "UNKNOWN_DOMAIN" {
				return fmt.Errorf("ZADT_VSP on this system has no transport service: deploy the current ZADT_VSP (vsp install zadt-vsp), which includes ZCL_VSP_TRANSPORT_SERVICE")
			}
			return &TransportServiceError{Action: action, Code: resp.Error.Code, Message: resp.Error.Message}
		}
		return fmt.Errorf("transport.%s failed", action)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("transport.%s: unreadable answer: %w", action, err)
	}
	return nil
}

// The tp step does not run in the ZADT_VSP session: tp is started over
// synchronous RFC, which an ABAP Push Channel may not do. add_to_buffer
// schedules background job ZVSP_TRANSPORT_BUFFER and answers with a ticket;
// buffer_result answers "pending" until the job has stored its result.
var (
	transportPollInterval = 2 * time.Second
	transportJobTimeout   = 5 * time.Minute
)

type transportJobStarted struct {
	Status string `json:"status"`
	Ticket string `json:"ticket"`
	Job    string `json:"job"`
}

// transportJob starts a buffer job with action and waits for its result,
// which is decoded into out.
func transportJob(ctx context.Context, ws TransportService, action string, params map[string]any, out any) error {
	var started transportJobStarted
	if err := transportCall(ctx, ws, action, params, time.Minute, &started); err != nil {
		return err
	}
	if started.Ticket == "" {
		return fmt.Errorf("transport.%s: ZADT_VSP started no job", action)
	}
	deadline := time.Now().Add(transportJobTimeout)
	for {
		var raw json.RawMessage
		if err := transportCall(ctx, ws, "buffer_result", map[string]any{"ticket": started.Ticket}, time.Minute, &raw); err != nil {
			return err
		}
		var st struct {
			Status    string `json:"status"`
			JobStatus string `json:"job_status"`
		}
		_ = json.Unmarshal(raw, &st)
		if st.Status != "pending" {
			return json.Unmarshal(raw, out)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("background job %s %s has not finished after %s (status %q); see SM37 -- what it did to the buffer is unknown",
				started.Job, started.Ticket, transportJobTimeout, st.JobStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(transportPollInterval):
		}
	}
}

// TransportServiceError is a refusal or failure reported by
// ZCL_VSP_TRANSPORT_SERVICE.
type TransportServiceError struct {
	Action, Code, Message string
}

func (e *TransportServiceError) Error() string {
	return fmt.Sprintf("transport.%s: %s: %s", e.Action, e.Code, e.Message)
}

type transportBeginAnswer struct {
	AssemblyID string `json:"assembly_id"`
	Request    string `json:"request"`
	System     string `json:"system"`
	Client     string `json:"client"`
}

type transportCommitAnswer struct {
	Request    string `json:"request"`
	CofilePath string `json:"cofile_path"`
	DataPath   string `json:"data_path"`
	CofileSize int    `json:"cofile_size"`
	DataSize   int    `json:"data_size"`
}

type transportAddAnswer struct {
	Request    string   `json:"request"`
	System     string   `json:"system"`
	Command    string   `json:"tp_command"`
	ReturnCode string   `json:"tp_rc"`
	Message    string   `json:"tp_message"`
	Stdout     []string `json:"stdout"`
	RolledBack bool     `json:"rolled_back"`
}

type transportBufferAnswer struct {
	System     string `json:"system"`
	Client     string `json:"client"`
	Total      int    `json:"total"`
	Truncated  bool   `json:"truncated"`
	Source     string `json:"source"`
	FileExists bool   `json:"file_exists"`
	Entries    []struct {
		Request    string `json:"trkorr"`
		Client     string `json:"tarcli"`
		SourceCli  string `json:"srccli"`
		Function   string `json:"trfunction"`
		Owner      string `json:"owner"`
		UModes     string `json:"umodes"`
		ReturnCode string `json:"retcode"`
		Step       string `json:"step"`
		ImpFlag    string `json:"impflg"`
		Raw        string `json:"raw"`
	} `json:"entries"`
	Command    string   `json:"tp_command"`
	ReturnCode string   `json:"tp_rc"`
	Message    string   `json:"tp_message"`
	Stdout     []string `json:"stdout"`
}

// sameClient compares two clients, an empty one being the default 001.
func sameClient(a, b string) bool {
	norm := func(c string) string {
		if c = strings.TrimSpace(c); c == "" {
			return "001"
		}
		return c
	}
	return norm(a) == norm(b)
}

// UploadTransport writes files into DIR_TRANS of the connected system and adds
// the request to that system's import buffer. It never imports.
//
// Order: the gates; begin (ZADT_VSP re-validates the names and sizes, checks
// that neither file exists and answers which system and client it is); a
// check that this is the client the client was configured for; the chunks;
// commit (SHA-256 and size of each file, cofile shape, write data file then
// cofile, and on any failure delete what this commit wrote); add to buffer
// (refused when the request is already there; when tp fails and the request
// is not in the buffer, the files this upload wrote are deleted again); and a
// read of the buffer to show the entry.
func (c *Client) UploadTransport(ctx context.Context, ws TransportService, files *TransportFiles) (*TransportUploadResult, error) {
	if files == nil {
		return nil, errors.New("no files to upload")
	}
	// The files are validated again: a caller may have built the struct.
	checked, err := NewTransportFiles(files.CofileName, files.Cofile, files.DataName, files.Data)
	if err != nil {
		return nil, err
	}
	if err := c.CheckTransportUpload(checked.Request); err != nil {
		return nil, err
	}
	files = checked
	res := &TransportUploadResult{
		Request: files.Request, CofileName: files.CofileName, DataName: files.DataName,
		CofileSize: len(files.Cofile), DataSize: len(files.Data),
	}

	var begin transportBeginAnswer
	if err := transportCall(ctx, ws, "upload_files", map[string]any{
		"step":          "begin",
		"cofile_name":   files.CofileName,
		"data_name":     files.DataName,
		"cofile_size":   len(files.Cofile),
		"data_size":     len(files.Data),
		"cofile_sha256": sha256Hex(files.Cofile),
		"data_sha256":   sha256Hex(files.Data),
	}, 60*time.Second, &begin); err != nil {
		return nil, err
	}
	res.System, res.Client = begin.System, begin.Client
	abort := func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = transportCall(actx, ws, "upload_files", map[string]any{"step": "abort", "assembly_id": begin.AssemblyID}, 30*time.Second, nil)
	}
	if begin.AssemblyID == "" {
		return nil, errors.New("transport.upload_files: ZADT_VSP answered begin without an assembly id")
	}
	if !strings.EqualFold(begin.Request, files.Request) {
		abort()
		return nil, fmt.Errorf("ZADT_VSP read the files as request %q, not %q; nothing was written", begin.Request, files.Request)
	}
	// The WebSocket goes to the server's own URL and client, so this holds
	// unless something in between routed it elsewhere. If it does not, the
	// files would land in another client's system than the one configured.
	if !sameClient(begin.Client, c.config.Client) {
		abort()
		return nil, fmt.Errorf("ZADT_VSP answered from client %s of %s, but this connection is configured for client %s; nothing was written",
			begin.Client, begin.System, strings.TrimSpace(c.config.Client))
	}

	for _, part := range []struct {
		kind string
		data []byte
	}{{"data", files.Data}, {"cofile", files.Cofile}} {
		for off := 0; off < len(part.data); off += transportUploadChunk {
			end := min(off+transportUploadChunk, len(part.data))
			if err := transportCall(ctx, ws, "upload_files", map[string]any{
				"step":        "chunk",
				"assembly_id": begin.AssemblyID,
				"file":        part.kind,
				"offset":      off,
				"chunk_b64":   base64.StdEncoding.EncodeToString(part.data[off:end]),
			}, 60*time.Second, nil); err != nil {
				abort()
				return nil, fmt.Errorf("sending the %s file at offset %d: %w; nothing was written", part.kind, off, err)
			}
		}
	}

	var commit transportCommitAnswer
	if err := transportCall(ctx, ws, "upload_files", map[string]any{
		"step": "commit", "assembly_id": begin.AssemblyID,
	}, 5*time.Minute, &commit); err != nil {
		return nil, err
	}
	res.FilesWritten = true
	res.CofilePath, res.DataPath = commit.CofilePath, commit.DataPath

	var add transportAddAnswer
	addErr := transportJob(ctx, ws, "add_to_buffer", map[string]any{"request": files.Request}, &add)
	if addErr != nil {
		var se *TransportServiceError
		if errors.As(addErr, &se) && se.Code == "ADD_FAILED_ROLLED_BACK" {
			res.FilesWritten, res.RolledBack = false, true
		}
		res.Note = "the request was not added to the import buffer"
		return res, addErr
	}
	res.Queued = true
	res.TP = &TransportTPResult{Command: add.Command, ReturnCode: add.ReturnCode, Message: add.Message, Stdout: add.Stdout}
	if buf, err := c.TransportBuffer(ctx, ws, files.Request); err == nil {
		if e, ok := buf.Contains(files.Request); ok {
			res.InBuffer = e
		}
	}
	res.Note = fmt.Sprintf("%s is in the import queue of %s and has NOT been imported. Import it, if at all, in STMS.", files.Request, res.System)
	return res, nil
}

// TransportBuffer reads the connected system's import buffer: ZADT_VSP reads
// the buffer file DIR_TRANS/buffer/<SID> through SAP's EPS read checks -- no
// tp, no job, no database write. With a request, only that request's entries
// are returned. It changes nothing.
func (c *Client) TransportBuffer(ctx context.Context, ws TransportService, request string) (*TransportBufferResult, error) {
	request = strings.ToUpper(strings.TrimSpace(request))
	if err := c.CheckTransportBufferRead(request, "TransportBuffer"); err != nil {
		return nil, err
	}
	params := map[string]any{}
	if request != "" {
		params["request"] = request
	}
	var a transportBufferAnswer
	if err := transportCall(ctx, ws, "show_buffer", params, time.Minute, &a); err != nil {
		return nil, err
	}
	out := &TransportBufferResult{System: a.System, Client: a.Client, Request: request, Total: a.Total, Truncated: a.Truncated,
		Source: a.Source, FileExists: a.FileExists,
		Entries: make([]TransportBufferEntry, 0, len(a.Entries))}
	for _, e := range a.Entries {
		out.Entries = append(out.Entries, TransportBufferEntry{
			Request: strings.TrimSpace(e.Request), Client: strings.TrimSpace(e.Client), SourceCli: strings.TrimSpace(e.SourceCli),
			Function: strings.TrimSpace(e.Function), Owner: strings.TrimSpace(e.Owner), UModes: strings.TrimSpace(e.UModes),
			ReturnCode: strings.TrimSpace(e.ReturnCode), Step: strings.TrimSpace(e.Step), ImpFlag: strings.TrimSpace(e.ImpFlag),
			Raw: strings.TrimSpace(e.Raw),
		})
	}
	return out, nil
}

type transportDownloadAnswer struct {
	Name     string `json:"name"`
	Size     int    `json:"size"`
	Offset   int    `json:"offset"`
	ChunkB64 string `json:"chunk_b64"`
}

// DownloadTransportFiles reads a request's cofile and data file from DIR_TRANS
// of the connected system. It changes nothing.
func (c *Client) DownloadTransportFiles(ctx context.Context, ws TransportService, request string) (*TransportFiles, error) {
	request = strings.ToUpper(strings.TrimSpace(request))
	if err := c.CheckTransportDownload(request); err != nil {
		return nil, err
	}
	cofileName, dataName, err := TransportFileNamesForRequest(request)
	if err != nil {
		return nil, err
	}
	read := func(kind string, limit int) ([]byte, error) {
		var out []byte
		for {
			var a transportDownloadAnswer
			if err := transportCall(ctx, ws, "download_files", map[string]any{
				"request": request, "file": kind, "offset": len(out), "length": transportDownloadChunk,
			}, 2*time.Minute, &a); err != nil {
				return nil, err
			}
			if a.Size > limit {
				return nil, fmt.Errorf("the %s file of %s is %d bytes, over the %d-byte limit", kind, request, a.Size, limit)
			}
			chunk, err := base64.StdEncoding.DecodeString(a.ChunkB64)
			if err != nil {
				return nil, fmt.Errorf("the %s file of %s: unreadable chunk: %w", kind, request, err)
			}
			if a.Offset != len(out) {
				return nil, fmt.Errorf("the %s file of %s: asked for offset %d, got %d", kind, request, len(out), a.Offset)
			}
			out = append(out, chunk...)
			if len(out) >= a.Size {
				if len(out) != a.Size {
					return nil, fmt.Errorf("the %s file of %s: read %d bytes of %d", kind, request, len(out), a.Size)
				}
				return out, nil
			}
			if len(chunk) == 0 {
				return nil, fmt.Errorf("the %s file of %s: no progress at offset %d of %d", kind, request, len(out), a.Size)
			}
		}
	}
	cofile, err := read("cofile", TransportCofileMaxBytes)
	if err != nil {
		return nil, err
	}
	data, err := read("data", TransportUploadMaxBytes)
	if err != nil {
		return nil, err
	}
	return &TransportFiles{
		Request: request, SID: request[:3], Number: request[4:],
		CofileName: cofileName, DataName: dataName, Cofile: cofile, Data: data,
	}, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
