"! <p class="shorttext synchronized">VSP Transport Upload Service</p>
"! Domain "transport": writes a released request's cofile K&lt;nr&gt;.&lt;SID&gt;
"! and data file R&lt;nr&gt;.&lt;SID&gt; into DIR_TRANS of this system and adds
"! the request to this system's import buffer. It never imports: the import
"! stays a human step in STMS.
"!
"! Rules, each pinned by a Go test over this source
"! (embedded/abap/transport_service_test.go):
"! - files are written only through EPS_OPEN_OUTPUT_FILE, EPS_WRITE_BLOCK and
"!   EPS_CLOSE_FILE, into the logical directories $TR_COFI and $TR_DATA; the
"!   caller names a file, never a directory or a path;
"! - a file that exists is never overwritten, a zero-byte one included, and a
"!   failure deletes only what this call wrote;
"! - tp is given one command, the literal ADDTOBUFFER, for sy-sysid, through
"!   TMS_TP_MAINTAIN_BUFFER, and only for the request this session uploaded;
"!   the job reads the buffer through TMS_TP_SHOW_BUFFER before it adds;
"! - show_buffer reads the buffer file DIR_TRANS/buffer/&lt;SID&gt; and nothing more;
"! - there is no dynamic CALL FUNCTION.
"! tp is started over synchronous RFC, which an ABAP Push Channel may not do
"! (APC_ILLEGAL_STATEMENT), so the add runs as a background job,
"! ZVSP_TRANSPORT_BUFFER, which calls run_job. The session hands the job its
"! work through INDX under the job's own number and polls for the result
"! with buffer_result; the job does nothing without such a ticket.
"! Authority is SAP's own: S_CTS_ADMI EPS1 for the files (EPS layer), TADD
"! for the buffer (tp interface). A refusal is reported, not worked around.
CLASS zcl_vsp_transport_service DEFINITION
  PUBLIC
  FINAL
  CREATE PUBLIC.

  PUBLIC SECTION.
    INTERFACES zif_vsp_service.

    "! Both files together, in bytes (50 MB).
    CONSTANTS c_max_total TYPE i VALUE 52428800.
    "! The cofile alone, in bytes.
    CONSTANTS c_max_cofile TYPE i VALUE 1048576.
    "! One decoded chunk, in bytes.
    CONSTANTS c_max_chunk TYPE i VALUE 1048576.
    "! Buffer lines returned by show_buffer.
    CONSTANTS c_max_buffer_entries TYPE i VALUE 500.
    "! The background job (and its program) that runs the tp step.
    CONSTANTS c_job_name TYPE tbtcjob-jobname VALUE 'ZVSP_TRANSPORT_BUFFER'.

    "! The background step: reads this job's ticket, reads the buffer and,
    "! for an ADD ticket, adds its request; stores the result for the session.
    "! Outside a ZVSP_TRANSPORT_BUFFER job, or without a ticket, it does nothing.
    CLASS-METHODS run_job.

    "! The request two file names belong to: ev_request is &lt;SID&gt;K&lt;nr&gt;.
    "! ev_error is set, and the rest initial, when they are not a pair.
    CLASS-METHODS request_from_names
      IMPORTING iv_cofile_name TYPE string
                iv_data_name   TYPE string
      EXPORTING ev_request     TYPE trkorr
                ev_sid         TYPE string
                ev_error       TYPE string.

    "! Initial when iv_content has the shape of a cofile of a request exported
    "! from iv_sid (the way STRF_READ_COFILE reads one); otherwise why not.
    CLASS-METHODS validate_cofile
      IMPORTING iv_content      TYPE xstring
                iv_sid          TYPE string
      RETURNING VALUE(rv_error) TYPE string.

  PRIVATE SECTION.
    TYPES:
      BEGIN OF ty_assembly,
        id          TYPE string,
        request     TYPE trkorr,
        sid         TYPE string,
        cofile_name TYPE string,
        data_name   TYPE string,
        cofile_size TYPE i,
        data_size   TYPE i,
        cofile_sha  TYPE string,
        data_sha    TYPE string,
        cofile      TYPE xstring,
        data        TYPE xstring,
      END OF ty_assembly,
      BEGIN OF ty_written,
        request     TYPE trkorr,
        cofile_name TYPE string,
        data_name   TYPE string,
      END OF ty_written,
      BEGIN OF ty_ticket,
        action  TYPE string,
        request TYPE string,
      END OF ty_ticket,
      BEGIN OF ty_job_result,
        action       TYPE string,
        request      TYPE string,
        system       TYPE string,
        code         TYPE string,
        message      TYPE string,
        tp_cmd       TYPE string,
        tp_rc        TYPE string,
        tp_msg       TYPE string,
        buffer_known TYPE abap_bool,
        in_buffer    TYPE abap_bool,
      END OF ty_job_result,
      BEGIN OF ty_pending,
        ticket  TYPE string,
        action  TYPE string,
        request TYPE string,
      END OF ty_pending,
      tt_tpbuffer TYPE STANDARD TABLE OF tpbuffer WITH DEFAULT KEY,
      tt_tpstdout TYPE STANDARD TABLE OF tpstdout WITH DEFAULT KEY.

    "! The upload being assembled in this session; one at a time.
    DATA ms_assembly TYPE ty_assembly.
    "! The files the last commit of this session wrote, until they are added
    "! to the buffer. Only this request may be added, and only these files
    "! are deleted when the add fails.
    DATA ms_written TYPE ty_written.
    "! The buffer job this session started and has not collected yet.
    DATA ms_pending TYPE ty_pending.

    METHODS handle_upload_files
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS upload_begin
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS upload_chunk
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS upload_commit
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    "! Discards the upload in progress -- only the one the caller names.
    METHODS upload_abort
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS handle_add_to_buffer
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS handle_show_buffer
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS handle_download_files
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    METHODS handle_buffer_result
      IMPORTING is_message         TYPE zif_vsp_service=>ty_message
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

    "! Schedules ZVSP_TRANSPORT_BUFFER with a ticket for one action.
    CLASS-METHODS start_job
      IMPORTING iv_action   TYPE string
                iv_request  TYPE string
      EXPORTING ev_jobcount TYPE string
                ev_error    TYPE string.

    CLASS-METHODS job_key
      IMPORTING iv_jobcount   TYPE csequence
      RETURNING VALUE(rv_key) TYPE indx-srtfd.

    "! The logical directory of a kind of file: $TR_COFI or $TR_DATA.
    CLASS-METHODS dir_of
      IMPORTING iv_kind       TYPE string
      RETURNING VALUE(rv_dir) TYPE epsf-epsdirnam.

    "! Whether a file of that kind and name exists in DIR_TRANS, at any size.
    CLASS-METHODS file_exists
      IMPORTING iv_kind          TYPE string
                iv_name          TYPE string
      EXPORTING ev_error         TYPE string
      RETURNING VALUE(rv_exists) TYPE abap_bool.

    "! Whether a file exists in a DIR_TRANS subdirectory. A failure to read
    "! the directory or the file's attributes is an error, never absence:
    "! absence is concluded only from a directory listing without it.
    CLASS-METHODS probe_file
      IMPORTING iv_subdir        TYPE epsf-epssubdir
                iv_name          TYPE string
      EXPORTING ev_error         TYPE string
                ev_long_dir      TYPE eps2path
      RETURNING VALUE(rv_exists) TYPE abap_bool.

    "! Writes a new file through the EPS layer. ev_opened says a file was
    "! created, so that a failure after it knows what to delete.
    CLASS-METHODS write_file
      IMPORTING iv_kind    TYPE string
                iv_name    TYPE string
                iv_content TYPE xstring
      EXPORTING ev_opened  TYPE abap_bool
                ev_path    TYPE string
                ev_error   TYPE string.

    CLASS-METHODS delete_file
      IMPORTING iv_kind TYPE string
                iv_name TYPE string.

    "! This system's import buffer, read-only.
    CLASS-METHODS read_buffer
      EXPORTING et_buffer TYPE tt_tpbuffer
                et_stdout TYPE tt_tpstdout
                ev_cmd    TYPE string
                ev_rc     TYPE string
                ev_msg    TYPE string
                ev_error  TYPE string.

    CLASS-METHODS request_parts
      IMPORTING iv_request TYPE string
      EXPORTING ev_sid     TYPE string
                ev_number  TYPE string
      RETURNING VALUE(rv_ok) TYPE abap_bool.

    CLASS-METHODS sha256
      IMPORTING iv_data        TYPE xstring
      RETURNING VALUE(rv_hash) TYPE string.

    CLASS-METHODS last_message
      RETURNING VALUE(rv_text) TYPE string.

    CLASS-METHODS stdout_json
      IMPORTING it_stdout      TYPE tt_tpstdout
      RETURNING VALUE(rv_json) TYPE string.

    CLASS-METHODS err
      IMPORTING iv_id              TYPE string
                iv_code            TYPE string
                iv_message         TYPE string
      RETURNING VALUE(rs_response) TYPE zif_vsp_service=>ty_response.

ENDCLASS.


CLASS zcl_vsp_transport_service IMPLEMENTATION.

  METHOD zif_vsp_service~get_domain.
    rv_domain = 'transport'.
  ENDMETHOD.


  METHOD zif_vsp_service~handle_message.
    CASE is_message-action.
      WHEN 'upload_files'.
        rs_response = handle_upload_files( is_message ).
      WHEN 'add_to_buffer'.
        rs_response = handle_add_to_buffer( is_message ).
      WHEN 'show_buffer'.
        rs_response = handle_show_buffer( is_message ).
      WHEN 'download_files'.
        rs_response = handle_download_files( is_message ).
      WHEN 'buffer_result'.
        rs_response = handle_buffer_result( is_message ).
      WHEN OTHERS.
        rs_response = err( iv_id = is_message-id iv_code = 'UNKNOWN_ACTION'
                           iv_message = |Action '{ is_message-action }' not supported| ).
    ENDCASE.
  ENDMETHOD.


  METHOD zif_vsp_service~on_disconnect.
    CLEAR: ms_assembly, ms_written, ms_pending.
  ENDMETHOD.


  METHOD handle_upload_files.
    DATA(lv_step) = zcl_vsp_utils=>extract_param( iv_params = is_message-params iv_name = 'step' ).
    CASE lv_step.
      WHEN 'begin'.
        rs_response = upload_begin( is_message ).
      WHEN 'chunk'.
        rs_response = upload_chunk( is_message ).
      WHEN 'commit'.
        rs_response = upload_commit( is_message ).
      WHEN 'abort'.
        rs_response = upload_abort( is_message ).
      WHEN OTHERS.
        rs_response = err( iv_id = is_message-id iv_code = 'INVALID_PARAM'
                           iv_message = |step must be begin, chunk, commit or abort, not '{ lv_step }'| ).
    ENDCASE.
  ENDMETHOD.


  METHOD upload_begin.
    DATA: lv_request TYPE trkorr,
          lv_sid     TYPE string,
          lv_error   TYPE string,
          lv_uuid    TYPE sysuuid_c32.

    CLEAR ms_assembly.
    DATA(lv_params) = is_message-params.
    DATA(lv_cofile_name) = zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'cofile_name' ).
    DATA(lv_data_name) = zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'data_name' ).
    DATA(lv_cofile_size) = zcl_vsp_utils=>extract_param_int( iv_params = lv_params iv_name = 'cofile_size' ).
    DATA(lv_data_size) = zcl_vsp_utils=>extract_param_int( iv_params = lv_params iv_name = 'data_size' ).
    DATA(lv_cofile_sha) = to_upper( zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'cofile_sha256' ) ).
    DATA(lv_data_sha) = to_upper( zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'data_sha256' ) ).

    request_from_names( EXPORTING iv_cofile_name = lv_cofile_name iv_data_name = lv_data_name
                        IMPORTING ev_request = lv_request ev_sid = lv_sid ev_error = lv_error ).
    IF lv_error IS NOT INITIAL.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_NAMES' iv_message = lv_error ).
      RETURN.
    ENDIF.

    IF lv_cofile_size <= 0 OR lv_data_size <= 0.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_PARAM'
                         iv_message = `Both files are required and neither may be empty` ).
      RETURN.
    ENDIF.
    IF lv_cofile_size > c_max_cofile.
      rs_response = err( iv_id = is_message-id iv_code = 'TOO_LARGE'
                         iv_message = |The cofile is { lv_cofile_size } bytes, over the { c_max_cofile }-byte limit| ).
      RETURN.
    ENDIF.
    IF lv_data_size > c_max_total - lv_cofile_size.
      rs_response = err( iv_id = is_message-id iv_code = 'TOO_LARGE'
                         iv_message = |The two files are over the { c_max_total }-byte (50 MB) limit| ).
      RETURN.
    ENDIF.
    FIND PCRE '^[0-9A-F]{64}\z' IN lv_cofile_sha.
    DATA(lv_ok1) = xsdbool( sy-subrc = 0 ).
    FIND PCRE '^[0-9A-F]{64}\z' IN lv_data_sha.
    IF lv_ok1 = abap_false OR sy-subrc <> 0.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_PARAM'
                         iv_message = `cofile_sha256 and data_sha256 must be SHA-256 digests in hex` ).
      RETURN.
    ENDIF.

    " Neither file may exist, not even empty: an upload never replaces one.
    DATA(lt_kinds) = VALUE string_table( ( `data` ) ( `cofile` ) ).
    LOOP AT lt_kinds INTO DATA(lv_kind).
      DATA(lv_name) = COND string( WHEN lv_kind = `cofile` THEN lv_cofile_name ELSE lv_data_name ).
      DATA(lv_exists) = file_exists( EXPORTING iv_kind = lv_kind iv_name = lv_name IMPORTING ev_error = lv_error ).
      IF lv_error IS NOT INITIAL.
        rs_response = err( iv_id = is_message-id iv_code = 'DIR_TRANS_ERROR' iv_message = lv_error ).
        RETURN.
      ENDIF.
      IF lv_exists = abap_true.
        rs_response = err( iv_id = is_message-id iv_code = 'FILE_EXISTS'
                           iv_message = |{ lv_name } already exists in DIR_TRANS ({ dir_of( lv_kind ) }); it is never overwritten. Nothing was written.| ).
        RETURN.
      ENDIF.
    ENDLOOP.

    TRY.
        lv_uuid = cl_system_uuid=>create_uuid_c32_static( ).
      CATCH cx_uuid_error.
        lv_uuid = |TR{ sy-datum }{ sy-uzeit }|.
    ENDTRY.

    ms_assembly = VALUE #(
      id          = lv_uuid
      request     = lv_request
      sid         = lv_sid
      cofile_name = lv_cofile_name
      data_name   = lv_data_name
      cofile_size = lv_cofile_size
      data_size   = lv_data_size
      cofile_sha  = lv_cofile_sha
      data_sha    = lv_data_sha ).

    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
      ( zcl_vsp_utils=>json_str( iv_key = 'assembly_id' iv_value = ms_assembly-id ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'request' iv_value = CONV #( lv_request ) ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'system' iv_value = CONV #( sy-sysid ) ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'client' iv_value = CONV #( sy-mandt ) ) )
      ( zcl_vsp_utils=>json_int( iv_key = 'max_chunk' iv_value = c_max_chunk ) )
    ) ) ) ).
  ENDMETHOD.


  METHOD upload_chunk.
    DATA lv_chunk TYPE xstring.

    DATA(lv_params) = is_message-params.
    DATA(lv_id) = zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'assembly_id' ).
    IF ms_assembly-id IS INITIAL OR lv_id <> ms_assembly-id.
      rs_response = err( iv_id = is_message-id iv_code = 'NO_UPLOAD'
                         iv_message = `No upload with this assembly_id is in progress in this session` ).
      RETURN.
    ENDIF.
    DATA(lv_kind) = zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'file' ).
    DATA(lv_offset) = zcl_vsp_utils=>extract_param_int( iv_params = lv_params iv_name = 'offset' ).
    DATA(lv_b64) = zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'chunk_b64' ).
    TRY.
        lv_chunk = cl_http_utility=>decode_x_base64( lv_b64 ).
      CATCH cx_root.
        CLEAR lv_chunk.
    ENDTRY.
    IF xstrlen( lv_chunk ) = 0 OR xstrlen( lv_chunk ) > c_max_chunk.
      CLEAR ms_assembly.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_CHUNK'
                         iv_message = |A chunk is 1 to { c_max_chunk } bytes of base64; the upload is discarded| ).
      RETURN.
    ENDIF.

    CASE lv_kind.
      WHEN 'data'.
        IF lv_offset <> xstrlen( ms_assembly-data ) OR lv_offset + xstrlen( lv_chunk ) > ms_assembly-data_size.
          CLEAR ms_assembly.
          rs_response = err( iv_id = is_message-id iv_code = 'INVALID_CHUNK'
                             iv_message = `Data chunk out of order or past the declared size; the upload is discarded` ).
          RETURN.
        ENDIF.
        CONCATENATE ms_assembly-data lv_chunk INTO ms_assembly-data IN BYTE MODE.
      WHEN 'cofile'.
        IF lv_offset <> xstrlen( ms_assembly-cofile ) OR lv_offset + xstrlen( lv_chunk ) > ms_assembly-cofile_size.
          CLEAR ms_assembly.
          rs_response = err( iv_id = is_message-id iv_code = 'INVALID_CHUNK'
                             iv_message = `Cofile chunk out of order or past the declared size; the upload is discarded` ).
          RETURN.
        ENDIF.
        CONCATENATE ms_assembly-cofile lv_chunk INTO ms_assembly-cofile IN BYTE MODE.
      WHEN OTHERS.
        CLEAR ms_assembly.
        rs_response = err( iv_id = is_message-id iv_code = 'INVALID_PARAM'
                           iv_message = `file must be cofile or data; the upload is discarded` ).
        RETURN.
    ENDCASE.

    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
      ( zcl_vsp_utils=>json_int( iv_key = 'cofile_received' iv_value = xstrlen( ms_assembly-cofile ) ) )
      ( zcl_vsp_utils=>json_int( iv_key = 'data_received' iv_value = xstrlen( ms_assembly-data ) ) )
    ) ) ) ).
  ENDMETHOD.


  METHOD upload_abort.
    DATA(lv_id) = zcl_vsp_utils=>extract_param( iv_params = is_message-params iv_name = 'assembly_id' ).
    IF ms_assembly-id IS INITIAL OR lv_id <> ms_assembly-id.
      rs_response = err( iv_id = is_message-id iv_code = 'NO_UPLOAD'
                         iv_message = `No upload with this assembly_id is in progress in this session; nothing was discarded` ).
      RETURN.
    ENDIF.
    CLEAR ms_assembly.
    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id
      iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_bool( iv_key = 'aborted' iv_value = abap_true ) ) ).
  ENDMETHOD.


  METHOD upload_commit.
    DATA: lv_error       TYPE string,
          lv_data_open   TYPE abap_bool,
          lv_cofile_open TYPE abap_bool,
          lv_data_path   TYPE string,
          lv_cofile_path TYPE string.

    DATA(lv_id) = zcl_vsp_utils=>extract_param( iv_params = is_message-params iv_name = 'assembly_id' ).
    IF ms_assembly-id IS INITIAL OR lv_id <> ms_assembly-id.
      rs_response = err( iv_id = is_message-id iv_code = 'NO_UPLOAD'
                         iv_message = `No upload with this assembly_id is in progress in this session` ).
      RETURN.
    ENDIF.
    DATA(ls_up) = ms_assembly.
    CLEAR ms_assembly.

    IF xstrlen( ls_up-data ) <> ls_up-data_size OR xstrlen( ls_up-cofile ) <> ls_up-cofile_size.
      rs_response = err( iv_id = is_message-id iv_code = 'INCOMPLETE'
                         iv_message = |Received { xstrlen( ls_up-cofile ) } of { ls_up-cofile_size } cofile bytes and | &&
                                      |{ xstrlen( ls_up-data ) } of { ls_up-data_size } data bytes. Nothing was written.| ).
      RETURN.
    ENDIF.
    IF sha256( ls_up-data ) <> ls_up-data_sha OR sha256( ls_up-cofile ) <> ls_up-cofile_sha.
      rs_response = err( iv_id = is_message-id iv_code = 'CHECKSUM_MISMATCH'
                         iv_message = `The received bytes do not match the SHA-256 declared at begin. Nothing was written.` ).
      RETURN.
    ENDIF.
    lv_error = validate_cofile( iv_content = ls_up-cofile iv_sid = ls_up-sid ).
    IF lv_error IS NOT INITIAL.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_COFILE'
                         iv_message = |{ ls_up-cofile_name }: { lv_error }. Nothing was written.| ).
      RETURN.
    ENDIF.

    " Checked again: begin may have been a while ago.
    DATA(lt_kinds) = VALUE string_table( ( `data` ) ( `cofile` ) ).
    LOOP AT lt_kinds INTO DATA(lv_kind).
      DATA(lv_name) = COND string( WHEN lv_kind = `cofile` THEN ls_up-cofile_name ELSE ls_up-data_name ).
      DATA(lv_exists) = file_exists( EXPORTING iv_kind = lv_kind iv_name = lv_name IMPORTING ev_error = lv_error ).
      IF lv_error IS NOT INITIAL.
        rs_response = err( iv_id = is_message-id iv_code = 'DIR_TRANS_ERROR' iv_message = lv_error ).
        RETURN.
      ENDIF.
      IF lv_exists = abap_true.
        rs_response = err( iv_id = is_message-id iv_code = 'FILE_EXISTS'
                           iv_message = |{ lv_name } already exists in DIR_TRANS; it is never overwritten. Nothing was written.| ).
        RETURN.
      ENDIF.
    ENDLOOP.

    " The data file first, so that tp never finds a cofile without its data.
    write_file( EXPORTING iv_kind = `data` iv_name = ls_up-data_name iv_content = ls_up-data
                IMPORTING ev_opened = lv_data_open ev_path = lv_data_path ev_error = lv_error ).
    IF lv_error IS NOT INITIAL.
      IF lv_data_open = abap_true.
        delete_file( iv_kind = `data` iv_name = ls_up-data_name ).
      ENDIF.
      rs_response = err( iv_id = is_message-id iv_code = 'WRITE_FAILED'
                         iv_message = |{ ls_up-data_name }: { lv_error }. | &&
                                      COND string( WHEN lv_data_open = abap_true THEN `What this call wrote was deleted again.`
                                                   ELSE `Nothing was written.` ) ).
      RETURN.
    ENDIF.

    write_file( EXPORTING iv_kind = `cofile` iv_name = ls_up-cofile_name iv_content = ls_up-cofile
                IMPORTING ev_opened = lv_cofile_open ev_path = lv_cofile_path ev_error = lv_error ).
    IF lv_error IS NOT INITIAL.
      IF lv_cofile_open = abap_true.
        delete_file( iv_kind = `cofile` iv_name = ls_up-cofile_name ).
      ENDIF.
      delete_file( iv_kind = `data` iv_name = ls_up-data_name ).
      rs_response = err( iv_id = is_message-id iv_code = 'WRITE_FAILED'
                         iv_message = |{ ls_up-cofile_name }: { lv_error }. What this call wrote was deleted again.| ).
      RETURN.
    ENDIF.

    ms_written = VALUE #( request = ls_up-request cofile_name = ls_up-cofile_name data_name = ls_up-data_name ).

    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
      ( zcl_vsp_utils=>json_str( iv_key = 'request' iv_value = CONV #( ls_up-request ) ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'cofile_path' iv_value = lv_cofile_path ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'data_path' iv_value = lv_data_path ) )
      ( zcl_vsp_utils=>json_int( iv_key = 'cofile_size' iv_value = ls_up-cofile_size ) )
      ( zcl_vsp_utils=>json_int( iv_key = 'data_size' iv_value = ls_up-data_size ) )
    ) ) ) ).
  ENDMETHOD.


  METHOD handle_add_to_buffer.
    DATA: lv_sid      TYPE string,
          lv_number   TYPE string,
          lv_error    TYPE string,
          lv_trkorr   TYPE trkorr,
          lv_jobcount TYPE string.

    DATA(lv_request) = to_upper( zcl_vsp_utils=>extract_param( iv_params = is_message-params iv_name = 'request' ) ).
    IF request_parts( EXPORTING iv_request = lv_request IMPORTING ev_sid = lv_sid ev_number = lv_number ) = abap_false.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_REQUEST'
                         iv_message = |Request '{ lv_request }' is not <SID>K<6 digits>| ).
      RETURN.
    ENDIF.
    lv_trkorr = lv_request.

    " Only the request whose files this session's upload wrote.
    IF ms_written-request IS INITIAL OR ms_written-request <> lv_trkorr.
      rs_response = err( iv_id = is_message-id iv_code = 'NOT_UPLOADED'
                         iv_message = |{ lv_request } was not uploaded in this session; only an uploaded request is added to the buffer| ).
      RETURN.
    ENDIF.
    IF ms_pending-ticket IS NOT INITIAL.
      rs_response = err( iv_id = is_message-id iv_code = 'BUSY'
                         iv_message = |Buffer job { ms_pending-ticket } of this session has not been collected yet (buffer_result)| ).
      RETURN.
    ENDIF.

    DATA(lt_kinds) = VALUE string_table( ( `data` ) ( `cofile` ) ).
    LOOP AT lt_kinds INTO DATA(lv_kind).
      DATA(lv_name) = COND string( WHEN lv_kind = `cofile` THEN ms_written-cofile_name ELSE ms_written-data_name ).
      IF file_exists( EXPORTING iv_kind = lv_kind iv_name = lv_name IMPORTING ev_error = lv_error ) = abap_false.
        CLEAR ms_written.
        rs_response = err( iv_id = is_message-id iv_code = 'FILES_MISSING'
                           iv_message = |{ lv_name } is not in DIR_TRANS { lv_error }; tp needs both files| ).
        RETURN.
      ENDIF.
    ENDLOOP.

    start_job( EXPORTING iv_action = `ADD` iv_request = lv_request
               IMPORTING ev_jobcount = lv_jobcount ev_error = lv_error ).
    IF lv_error IS NOT INITIAL.
      " Nothing reached tp: the request is not in the buffer, so this
      " upload's files go again.
      delete_file( iv_kind = `cofile` iv_name = ms_written-cofile_name ).
      delete_file( iv_kind = `data` iv_name = ms_written-data_name ).
      CLEAR ms_written.
      rs_response = err( iv_id = is_message-id iv_code = 'ADD_FAILED_ROLLED_BACK'
                         iv_message = |The buffer job could not be started: { lv_error }. The files this upload wrote were deleted again.| ).
      RETURN.
    ENDIF.
    ms_pending = VALUE #( ticket = lv_jobcount action = `ADD` request = lv_request ).

    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
      ( zcl_vsp_utils=>json_str( iv_key = 'status' iv_value = `started` ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'ticket' iv_value = lv_jobcount ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'job' iv_value = CONV #( c_job_name ) ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'request' iv_value = lv_request ) )
    ) ) ) ).
  ENDMETHOD.


  METHOD handle_show_buffer.
    " The import buffer is a file, DIR_TRANS/buffer/<SID>. It is read here,
    " through SAP's EPS read checks, and nothing else is done: no tp, no
    " job, no database write.
    DATA: lv_sid        TYPE string,
          lv_number     TYPE string,
          lv_name       TYPE eps2filnam,
          lv_buffer_dir TYPE epsf-epsdirnam,
          lv_long_dir   TYPE eps2path,
          lv_path       TYPE eps2path,
          lv_size       TYPE eps2filsiz,
          lv_pos        TYPE epsfilsiz,
          lv_content    TYPE xstring,
          lv_text       TYPE string,
          lv_dataset    TYPE string,
          lt_lines      TYPE string_table,
          lt_tok        TYPE string_table,
          lt_items      TYPE string_table,
          lv_total      TYPE i.

    DATA(lv_request) = to_upper( zcl_vsp_utils=>extract_param( iv_params = is_message-params iv_name = 'request' ) ).
    IF lv_request IS NOT INITIAL AND
       request_parts( EXPORTING iv_request = lv_request IMPORTING ev_sid = lv_sid ev_number = lv_number ) = abap_false.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_REQUEST'
                         iv_message = |Request '{ lv_request }' is not <SID>K<6 digits>| ).
      RETURN.
    ENDIF.

    lv_name = sy-sysid.
    lv_buffer_dir = '$TR_BUFF'.
    DATA lv_probe_error TYPE string.
    DATA(lv_exists) = probe_file( EXPORTING iv_subdir = CONV #( lv_buffer_dir ) iv_name = CONV #( lv_name )
                                  IMPORTING ev_error = lv_probe_error ).
    IF lv_probe_error IS NOT INITIAL.
      rs_response = err( iv_id = is_message-id iv_code = 'BUFFER_READ_FAILED'
                         iv_message = |The import buffer file { lv_name } in DIR_TRANS ($TR_BUFF) could not be read: { lv_probe_error }| ).
      RETURN.
    ENDIF.

    IF lv_exists = abap_true.
      CALL FUNCTION 'EPS_OPEN_INPUT_FILE'
        EXPORTING
          iv_long_file_name      = lv_name
          dir_name               = lv_buffer_dir
          pos                    = lv_pos
        IMPORTING
          ev_long_dir_name       = lv_long_dir
          ev_long_file_path      = lv_path
          ev_file_size_long      = lv_size
        EXCEPTIONS
          invalid_eps_subdir     = 1
          sapgparam_failed       = 2
          build_directory_failed = 3
          no_authorization       = 4
          build_path_failed      = 5
          open_failed            = 6
          read_directory_failed  = 7
          read_attributes_failed = 8
          OTHERS                 = 9.
      DATA(lv_subrc) = sy-subrc.
      IF lv_subrc <> 0.
        " It exists; failing to open it is an error, not an empty buffer.
        rs_response = err( iv_id = is_message-id
                           iv_code = COND #( WHEN lv_subrc = 4 THEN `NO_AUTHORIZATION` ELSE `BUFFER_READ_FAILED` )
                           iv_message = |The import buffer file { lv_name } in DIR_TRANS ($TR_BUFF) could not be read (exception { lv_subrc }) { last_message( ) }| ).
        RETURN.
      ENDIF.
      CALL FUNCTION 'EPS_CLOSE_FILE'
        EXPORTING
          iv_long_file_name = lv_name
          iv_long_dir_name  = lv_long_dir
        EXCEPTIONS
          OTHERS            = 1.
      IF lv_size > c_max_total.
        rs_response = err( iv_id = is_message-id iv_code = 'TOO_LARGE'
                           iv_message = |The import buffer file is { lv_size } bytes, over the { c_max_total }-byte limit| ).
        RETURN.
      ENDIF.
      lv_dataset = lv_path.
      TRY.
          OPEN DATASET lv_dataset FOR INPUT IN BINARY MODE.
          IF sy-subrc <> 0.
            rs_response = err( iv_id = is_message-id iv_code = 'BUFFER_READ_FAILED'
                               iv_message = |The import buffer file { lv_name } could not be opened| ).
            RETURN.
          ENDIF.
          READ DATASET lv_dataset INTO lv_content.
          CLOSE DATASET lv_dataset.
          lv_text = cl_abap_codepage=>convert_from( source = lv_content codepage = `UTF-8` ).
        CATCH cx_root INTO DATA(lx_read).
          CLOSE DATASET lv_dataset.
          rs_response = err( iv_id = is_message-id iv_code = 'BUFFER_READ_FAILED'
                             iv_message = |The import buffer file { lv_name }: { lx_read->get_text( ) }| ).
          RETURN.
      ENDTRY.
    ENDIF.
    " No buffer file (listed directory without it): nothing was ever queued.

    " One request per line: [/<n>/]<TRKORR> <type><release> <owner> ... ;
    " lines starting with '#' are comments.
    SPLIT lv_text AT cl_abap_char_utilities=>newline INTO TABLE lt_lines.
    LOOP AT lt_lines INTO DATA(lv_line).
      REPLACE ALL OCCURRENCES OF cl_abap_char_utilities=>cr_lf(1) IN lv_line WITH ``.
      DATA(lv_raw) = condense( lv_line ).
      IF lv_raw IS INITIAL OR lv_raw(1) = '#'.
        CONTINUE.
      ENDIF.
      SPLIT lv_raw AT space INTO TABLE lt_tok.
      DATA(lv_req) = lt_tok[ 1 ].
      REPLACE PCRE '^/[^/]*/' IN lv_req WITH ``.
      IF lv_request IS NOT INITIAL AND lv_req <> lv_request.
        CONTINUE.
      ENDIF.
      lv_total = lv_total + 1.
      IF lv_total > c_max_buffer_entries.
        CONTINUE.
      ENDIF.
      DATA(lv_type) = COND string( WHEN lines( lt_tok ) >= 2 AND strlen( lt_tok[ 2 ] ) >= 1 THEN substring( val = lt_tok[ 2 ] len = 1 ) ).
      DATA(lv_owner) = COND string( WHEN lines( lt_tok ) >= 3 THEN lt_tok[ 3 ] ).
      APPEND zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
        ( zcl_vsp_utils=>json_str( iv_key = 'trkorr' iv_value = lv_req ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'trfunction' iv_value = lv_type ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'owner' iv_value = lv_owner ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'raw' iv_value = lv_raw ) )
      ) ) ) TO lt_items.
    ENDLOOP.

    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
      ( zcl_vsp_utils=>json_str( iv_key = 'status' iv_value = `done` ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'system' iv_value = CONV #( sy-sysid ) ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'client' iv_value = CONV #( sy-mandt ) ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'source' iv_value = |DIR_TRANS/buffer/{ sy-sysid }| ) )
      ( zcl_vsp_utils=>json_bool( iv_key = 'file_exists' iv_value = lv_exists ) )
      ( zcl_vsp_utils=>json_int( iv_key = 'total' iv_value = lv_total ) )
      ( zcl_vsp_utils=>json_bool( iv_key = 'truncated' iv_value = xsdbool( lv_total > c_max_buffer_entries ) ) )
      ( |"entries":{ zcl_vsp_utils=>json_arr( zcl_vsp_utils=>json_join( lt_items ) ) }| )
    ) ) ) ).
  ENDMETHOD.


  METHOD handle_buffer_result.
    DATA: ls_res    TYPE ty_job_result,
          lt_buffer TYPE tt_tpbuffer,
          lt_stdout TYPE tt_tpstdout,
          ls_indx   TYPE indx,
          lv_status TYPE tbtco-status.

    DATA(lv_ticket) = zcl_vsp_utils=>extract_param( iv_params = is_message-params iv_name = 'ticket' ).
    IF ms_pending-ticket IS INITIAL OR lv_ticket <> ms_pending-ticket.
      rs_response = err( iv_id = is_message-id iv_code = 'NO_SUCH_JOB'
                         iv_message = |No buffer job { lv_ticket } is pending in this session| ).
      RETURN.
    ENDIF.
    DATA(lv_key) = job_key( lv_ticket ).

    IMPORT result = ls_res buffer = lt_buffer stdout = lt_stdout
      FROM DATABASE indx(zu) TO ls_indx ID lv_key.
    IF sy-subrc <> 0.
      SELECT SINGLE status FROM tbtco INTO @lv_status
        WHERE jobname = @c_job_name AND jobcount = @lv_ticket.
      IF sy-subrc <> 0 OR lv_status = 'A'.
        DATA(ls_lost) = ms_pending.
        CLEAR: ms_pending, ms_written.
        rs_response = err( iv_id = is_message-id iv_code = 'JOB_FAILED'
                           iv_message = |Background job { c_job_name } { lv_ticket } ended without a result (status '{ lv_status }'); see SM37.| &&
                                        COND string( WHEN ls_lost-action = `ADD`
                                                     THEN | Whether { ls_lost-request } reached the buffer is unknown; the uploaded files were kept.| ) ).
        RETURN.
      ENDIF.
      rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
        ( zcl_vsp_utils=>json_str( iv_key = 'status' iv_value = `pending` ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'job_status' iv_value = CONV #( lv_status ) ) )
      ) ) ) ).
      RETURN.
    ENDIF.

    DELETE FROM DATABASE indx(zu) ID lv_key.
    DATA(ls_pend) = ms_pending.
    CLEAR ms_pending.

    " ADD
    IF ls_res-code IS INITIAL.
      CLEAR ms_written.
      rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
        ( zcl_vsp_utils=>json_str( iv_key = 'status' iv_value = `done` ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'request' iv_value = ls_res-request ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'system' iv_value = ls_res-system ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'tp_command' iv_value = ls_res-tp_cmd ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'tp_rc' iv_value = ls_res-tp_rc ) )
        ( zcl_vsp_utils=>json_str( iv_key = 'tp_message' iv_value = ls_res-tp_msg ) )
        ( |"stdout":{ stdout_json( lt_stdout ) }| )
      ) ) ) ).
      RETURN.
    ENDIF.

    " Undo this upload's files, but only when the request is certainly not
    " in the buffer: a buffer line without its cofile is worse than both.
    IF ls_res-buffer_known = abap_true AND ls_res-in_buffer = abap_false AND ms_written-request = ls_pend-request.
      delete_file( iv_kind = `cofile` iv_name = ms_written-cofile_name ).
      delete_file( iv_kind = `data` iv_name = ms_written-data_name ).
      CLEAR ms_written.
      rs_response = err( iv_id = is_message-id iv_code = 'ADD_FAILED_ROLLED_BACK'
                         iv_message = ls_res-message && ` The files this upload wrote were deleted again.` ).
      RETURN.
    ENDIF.
    CLEAR ms_written.
    rs_response = err( iv_id = is_message-id iv_code = ls_res-code
                       iv_message = ls_res-message && COND string( WHEN ls_res-code <> `ALREADY_IN_BUFFER`
                                                                   THEN ` The uploaded files were kept: the buffer could not be confirmed free of the request.` ) ).
  ENDMETHOD.


  METHOD start_job.
    DATA: lv_jobname  TYPE tbtcjob-jobname,
          lv_jobcount TYPE tbtcjob-jobcount,
          lv_released TYPE btch0000-char1,
          ls_indx     TYPE indx.

    CLEAR: ev_jobcount, ev_error.
    lv_jobname = c_job_name.
    CALL FUNCTION 'JOB_OPEN'
      EXPORTING
        jobname          = lv_jobname
      IMPORTING
        jobcount         = lv_jobcount
      EXCEPTIONS
        cant_create_job  = 1
        invalid_job_data = 2
        jobname_missing  = 3
        OTHERS           = 4.
    IF sy-subrc <> 0.
      ev_error = |JOB_OPEN failed (exception { sy-subrc }) { last_message( ) }|.
      RETURN.
    ENDIF.

    " The ticket: what the job is to do, under the job's own number.
    DATA(ls_ticket) = VALUE ty_ticket( action = iv_action request = iv_request ).
    DATA(lv_key) = job_key( lv_jobcount ).
    EXPORT ticket = ls_ticket TO DATABASE indx(zt) FROM ls_indx ID lv_key.

    CALL FUNCTION 'JOB_SUBMIT'
      EXPORTING
        authcknam         = sy-uname
        jobcount          = lv_jobcount
        jobname           = lv_jobname
        report            = 'ZVSP_TRANSPORT_BUFFER'
      EXCEPTIONS
        bad_priparams     = 1
        bad_xpgflags      = 2
        invalid_jobdata   = 3
        jobname_missing   = 4
        job_notex         = 5
        job_submit_failed = 6
        lock_failed       = 7
        OTHERS            = 8.
    IF sy-subrc <> 0.
      ev_error = |JOB_SUBMIT failed (exception { sy-subrc }) { last_message( ) }|.
      DELETE FROM DATABASE indx(zt) ID lv_key.
      RETURN.
    ENDIF.

    " A start time within ten seconds is an immediate start (JOB_CLOSE).
    CALL FUNCTION 'JOB_CLOSE'
      EXPORTING
        jobcount             = lv_jobcount
        jobname              = lv_jobname
        sdlstrtdt            = sy-datum
        sdlstrttm            = sy-uzeit
      IMPORTING
        job_was_released     = lv_released
      EXCEPTIONS
        cant_start_immediate = 1
        invalid_startdate    = 2
        jobname_missing      = 3
        job_close_failed     = 4
        job_nosteps          = 5
        job_notex            = 6
        lock_failed          = 7
        invalid_target       = 8
        invalid_time_zone    = 9
        OTHERS               = 10.
    IF sy-subrc <> 0.
      ev_error = |JOB_CLOSE failed (exception { sy-subrc }) { last_message( ) }|.
      DELETE FROM DATABASE indx(zt) ID lv_key.
      RETURN.
    ENDIF.
    IF lv_released IS INITIAL.
      ev_error = |job { lv_jobname } { lv_jobcount } was scheduled but not released: releasing it needs S_BTCH_JOB with JOBACTION RELE; delete it in SM37|.
      DELETE FROM DATABASE indx(zt) ID lv_key.
      RETURN.
    ENDIF.
    ev_jobcount = lv_jobcount.
  ENDMETHOD.


  METHOD job_key.
    rv_key = |ZVSPTR{ iv_jobcount }|.
  ENDMETHOD.


  METHOD run_job.
    DATA: lv_jobcount TYPE tbtcm-jobcount,
          lv_jobname  TYPE tbtcm-jobname,
          ls_ticket   TYPE ty_ticket,
          ls_res      TYPE ty_job_result,
          lt_buffer   TYPE tt_tpbuffer,
          lt_stdout   TYPE tt_tpstdout,
          lt_addout   TYPE tt_tpstdout,
          ls_indx     TYPE indx,
          lv_error    TYPE string,
          lv_sid      TYPE string,
          lv_number   TYPE string,
          lv_trkorr   TYPE trkorr,
          lv_cmd      TYPE stpa-cmdstring,
          lv_rc       TYPE stpa-retcode,
          lv_msg      TYPE stpa-message,
          lv_system   TYPE stpa-sysname.

    " This system, and only ever this system (STPA-SYSNAME is CHAR10).
    lv_system = sy-sysid.

    CALL FUNCTION 'GET_JOB_RUNTIME_INFO'
      IMPORTING
        jobcount        = lv_jobcount
        jobname         = lv_jobname
      EXCEPTIONS
        no_runtime_info = 1
        OTHERS          = 2.
    IF sy-subrc <> 0 OR lv_jobname <> c_job_name.
      RETURN.
    ENDIF.
    DATA(lv_key) = job_key( lv_jobcount ).

    " The session commits the ticket when its message step ends, which may be
    " a moment after the job started.
    DO 30 TIMES.
      IMPORT ticket = ls_ticket FROM DATABASE indx(zt) TO ls_indx ID lv_key.
      IF sy-subrc = 0.
        EXIT.
      ENDIF.
      WAIT UP TO 2 SECONDS.
    ENDDO.
    IF ls_ticket IS INITIAL.
      RETURN.
    ENDIF.
    DELETE FROM DATABASE indx(zt) ID lv_key.
    COMMIT WORK.

    ls_res = VALUE #( action = ls_ticket-action request = ls_ticket-request system = CONV #( sy-sysid ) ).
    read_buffer( IMPORTING et_buffer = lt_buffer et_stdout = lt_stdout ev_cmd = ls_res-tp_cmd
                           ev_rc = ls_res-tp_rc ev_msg = ls_res-tp_msg ev_error = lv_error ).

    IF lv_error IS NOT INITIAL.
      ls_res-code = `BUFFER_READ_FAILED`.
      ls_res-message = |The import buffer of { sy-sysid } could not be read: { lv_error }|.
    ELSEIF ls_ticket-action = `ADD`.
      ls_res-buffer_known = abap_true.
      IF request_parts( EXPORTING iv_request = ls_ticket-request IMPORTING ev_sid = lv_sid ev_number = lv_number ) = abap_false.
        ls_res-code = `INVALID_REQUEST`.
        ls_res-message = |Request '{ ls_ticket-request }' is not <SID>K<6 digits>|.
      ELSE.
        lv_trkorr = ls_ticket-request.
        IF line_exists( lt_buffer[ trkorr = lv_trkorr ] ).
          " Never twice: a request already in the queue is left alone.
          ls_res-in_buffer = abap_true.
          ls_res-code = `ALREADY_IN_BUFFER`.
          ls_res-message = |{ lv_trkorr } is already in the import buffer of { sy-sysid }; it was not added again.|.
        ELSE.
          " The one tp command this class sends. The command is a literal,
          " the system is this one, and no option, client or flag is passed.
          CALL FUNCTION 'TMS_TP_MAINTAIN_BUFFER'
            EXPORTING
              iv_tp_command      = 'ADDTOBUFFER'
              iv_system_name     = lv_system
              iv_request         = lv_trkorr
            IMPORTING
              ev_tp_cmd_strg     = lv_cmd
              ev_tp_ret_code     = lv_rc
              ev_tp_message      = lv_msg
            TABLES
              tt_stdout          = lt_addout
            EXCEPTIONS
              invalid_command    = 1
              permission_denied  = 2
              tp_call_failed     = 3
              tp_interface_error = 4
              tp_reported_error  = 5
              OTHERS             = 6.
          DATA(lv_subrc) = sy-subrc.
          DATA(lv_sysmsg) = COND string( WHEN lv_subrc <> 0 THEN last_message( ) ).
          ls_res-tp_cmd = lv_cmd.
          ls_res-tp_rc = lv_rc.
          ls_res-tp_msg = lv_msg.
          lt_stdout = lt_addout.
          IF lv_subrc <> 0.
            ls_res-code = SWITCH string( lv_subrc
              WHEN 2 THEN `PERMISSION_DENIED`
              WHEN 3 THEN `TP_CALL_FAILED`
              WHEN 5 THEN `TP_REPORTED_ERROR`
              ELSE `ADD_FAILED` ).
            ls_res-message = |ADDTOBUFFER { lv_trkorr } { sy-sysid } failed ({ ls_res-code }, tp rc { lv_rc }): { lv_sysmsg } { lv_msg }|.
            IF lv_subrc = 2.
              ls_res-message = ls_res-message && ` -- adding to the buffer needs S_CTS_ADMI with CTS_ADMFCT TADD (or IMPT).`.
            ENDIF.
          ENDIF.
          " Read back: on success it shows the line, on failure it says
          " whether the files may be taken back.
          CLEAR lt_buffer.
          read_buffer( IMPORTING et_buffer = lt_buffer ev_error = lv_error ).
          ls_res-buffer_known = xsdbool( lv_error IS INITIAL ).
          ls_res-in_buffer = xsdbool( line_exists( lt_buffer[ trkorr = lv_trkorr ] ) ).
          DELETE lt_buffer WHERE trkorr <> lv_trkorr.
        ENDIF.
      ENDIF.
    ELSE.
      ls_res-code = `INVALID_PARAM`.
      ls_res-message = |Unknown job action '{ ls_ticket-action }'|.
    ENDIF.

    EXPORT result = ls_res buffer = lt_buffer stdout = lt_stdout TO DATABASE indx(zu) FROM ls_indx ID lv_key.
    COMMIT WORK.
  ENDMETHOD.


  METHOD handle_download_files.
    DATA: lv_sid      TYPE string,
          lv_number   TYPE string,
          lv_name     TYPE eps2filnam,
          lv_dir      TYPE epsf-epsdirnam,
          lv_long_dir TYPE eps2path,
          lv_path     TYPE eps2path,
          lv_size     TYPE eps2filsiz,
          lv_pos      TYPE epsfilsiz,
          lv_chunk    TYPE xstring,
          lv_dataset  TYPE string.

    DATA(lv_params) = is_message-params.
    DATA(lv_request) = to_upper( zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'request' ) ).
    IF request_parts( EXPORTING iv_request = lv_request IMPORTING ev_sid = lv_sid ev_number = lv_number ) = abap_false.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_REQUEST'
                         iv_message = |Request '{ lv_request }' is not <SID>K<6 digits>| ).
      RETURN.
    ENDIF.
    DATA(lv_kind) = zcl_vsp_utils=>extract_param( iv_params = lv_params iv_name = 'file' ).
    IF lv_kind <> `cofile` AND lv_kind <> `data`.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_PARAM' iv_message = `file must be cofile or data` ).
      RETURN.
    ENDIF.
    DATA(lv_offset) = zcl_vsp_utils=>extract_param_int( iv_params = lv_params iv_name = 'offset' ).
    DATA(lv_length) = zcl_vsp_utils=>extract_param_int( iv_params = lv_params iv_name = 'length' ).
    IF lv_offset < 0.
      rs_response = err( iv_id = is_message-id iv_code = 'INVALID_PARAM' iv_message = `offset must be a non-negative number` ).
      RETURN.
    ENDIF.
    IF lv_length <= 0 OR lv_length > c_max_chunk.
      lv_length = c_max_chunk.
    ENDIF.
    DATA(lv_limit) = COND i( WHEN lv_kind = `cofile` THEN c_max_cofile ELSE c_max_total ).

    lv_name = COND string( WHEN lv_kind = `cofile` THEN |K{ lv_number }.{ lv_sid }| ELSE |R{ lv_number }.{ lv_sid }| ).
    lv_dir = dir_of( lv_kind ).
    lv_pos = lv_offset.

    " SAP's own read checks (transport read authority, the transdir sub-path)
    " and the size; the file is then read here and closed again.
    CALL FUNCTION 'EPS_OPEN_INPUT_FILE'
      EXPORTING
        iv_long_file_name      = lv_name
        dir_name               = lv_dir
        pos                    = lv_pos
      IMPORTING
        ev_long_dir_name       = lv_long_dir
        ev_long_file_path      = lv_path
        ev_file_size_long      = lv_size
      EXCEPTIONS
        invalid_eps_subdir     = 1
        sapgparam_failed       = 2
        build_directory_failed = 3
        no_authorization       = 4
        build_path_failed      = 5
        open_failed            = 6
        read_directory_failed  = 7
        read_attributes_failed = 8
        OTHERS                 = 9.
    DATA(lv_subrc) = sy-subrc.
    IF lv_subrc <> 0.
      DATA(lv_code) = SWITCH string( lv_subrc WHEN 4 THEN `NO_AUTHORIZATION` WHEN 7 OR 8 THEN `FILE_NOT_FOUND` ELSE `READ_FAILED` ).
      rs_response = err( iv_id = is_message-id iv_code = lv_code
                         iv_message = |{ lv_name } in DIR_TRANS ({ lv_dir }): { lv_code } { last_message( ) }| ).
      RETURN.
    ENDIF.
    CALL FUNCTION 'EPS_CLOSE_FILE'
      EXPORTING
        iv_long_file_name = lv_name
        iv_long_dir_name  = lv_long_dir
      EXCEPTIONS
        OTHERS            = 1.

    IF lv_size > lv_limit.
      rs_response = err( iv_id = is_message-id iv_code = 'TOO_LARGE'
                         iv_message = |{ lv_name } is { lv_size } bytes, over the { lv_limit }-byte limit| ).
      RETURN.
    ENDIF.

    lv_dataset = lv_path.
    IF lv_offset < lv_size.
      TRY.
          OPEN DATASET lv_dataset FOR INPUT IN BINARY MODE AT POSITION lv_offset.
          IF sy-subrc = 0.
            READ DATASET lv_dataset INTO lv_chunk MAXIMUM LENGTH lv_length.
            CLOSE DATASET lv_dataset.
          ELSE.
            rs_response = err( iv_id = is_message-id iv_code = 'READ_FAILED' iv_message = |{ lv_name } could not be opened| ).
            RETURN.
          ENDIF.
        CATCH cx_root INTO DATA(lx_read).
          CLOSE DATASET lv_dataset.
          rs_response = err( iv_id = is_message-id iv_code = 'READ_FAILED' iv_message = |{ lv_name }: { lx_read->get_text( ) }| ).
          RETURN.
      ENDTRY.
    ENDIF.

    rs_response = zcl_vsp_utils=>build_success( iv_id = is_message-id iv_data = zcl_vsp_utils=>json_obj( zcl_vsp_utils=>json_join( VALUE #(
      ( zcl_vsp_utils=>json_str( iv_key = 'name' iv_value = CONV #( lv_name ) ) )
      ( |"size":{ lv_size }| )
      ( zcl_vsp_utils=>json_int( iv_key = 'offset' iv_value = lv_offset ) )
      ( zcl_vsp_utils=>json_str( iv_key = 'chunk_b64' iv_value = cl_http_utility=>encode_x_base64( lv_chunk ) ) )
    ) ) ) ).
  ENDMETHOD.


  METHOD request_from_names.
    DATA: lv_cnr  TYPE string,
          lv_csid TYPE string,
          lv_dnr  TYPE string,
          lv_dsid TYPE string.

    CLEAR: ev_request, ev_sid, ev_error.
    IF iv_cofile_name IS INITIAL OR iv_data_name IS INITIAL.
      ev_error = `Both files are required: the cofile K<nr>.<SID> and the data file R<nr>.<SID>`.
      RETURN.
    ENDIF.
    FIND PCRE '^K([0-9]{6})\.([A-Z0-9]{3})\z' IN iv_cofile_name SUBMATCHES lv_cnr lv_csid.
    IF sy-subrc <> 0.
      ev_error = |Cofile name '{ iv_cofile_name }' is not K<6 digits>.<SID>|.
      RETURN.
    ENDIF.
    FIND PCRE '^R([0-9]{6})\.([A-Z0-9]{3})\z' IN iv_data_name SUBMATCHES lv_dnr lv_dsid.
    IF sy-subrc <> 0.
      ev_error = |Data file name '{ iv_data_name }' is not R<6 digits>.<SID>|.
      RETURN.
    ENDIF.
    IF lv_cnr <> lv_dnr OR lv_csid <> lv_dsid.
      ev_error = |{ iv_cofile_name } and { iv_data_name } are not one request's files: number and SID must match|.
      RETURN.
    ENDIF.
    ev_sid = lv_csid.
    ev_request = |{ lv_csid }K{ lv_cnr }|.
  ENDMETHOD.


  METHOD validate_cofile.
    DATA: lv_text   TYPE string,
          lt_lines  TYPE string_table,
          lt_tok    TYPE string_table,
          lv_header TYPE abap_bool,
          lv_steps  TYPE i,
          lv_export TYPE abap_bool,
          lv_lineno TYPE i,
          lv_sys    TYPE string,
          lv_cli    TYPE string,
          lv_nul    TYPE x LENGTH 1 VALUE '00'.

    IF xstrlen( iv_content ) = 0.
      rv_error = `the cofile is empty`.
      RETURN.
    ENDIF.
    IF xstrlen( iv_content ) > c_max_cofile.
      rv_error = |the cofile is over the { c_max_cofile }-byte limit for a cofile|.
      RETURN.
    ENDIF.
    FIND lv_nul IN iv_content IN BYTE MODE.
    IF sy-subrc = 0.
      rv_error = `the cofile contains NUL bytes: it is not a cofile (is it the data file?)`.
      RETURN.
    ENDIF.
    TRY.
        lv_text = cl_abap_codepage=>convert_from( source = iv_content codepage = `UTF-8` ).
      CATCH cx_root.
        rv_error = `the cofile is not text`.
        RETURN.
    ENDTRY.
    FIND PCRE '[\x01-\x08\x0B\x0C\x0E-\x1F]' IN lv_text.
    IF sy-subrc = 0.
      rv_error = `the cofile contains control characters: it is not a cofile`.
      RETURN.
    ENDIF.

    SPLIT lv_text AT cl_abap_char_utilities=>newline INTO TABLE lt_lines.
    LOOP AT lt_lines INTO DATA(lv_line).
      lv_lineno = sy-tabix.
      REPLACE ALL OCCURRENCES OF cl_abap_char_utilities=>cr_lf(1) IN lv_line WITH ``.
      REPLACE ALL OCCURRENCES OF cl_abap_char_utilities=>horizontal_tab IN lv_line WITH ` `.
      CONDENSE lv_line.
      IF lv_line IS INITIAL.
        CONTINUE.
      ENDIF.
      IF lv_line(1) = '#'.
        CONTINUE.
      ENDIF.
      SPLIT lv_line AT space INTO TABLE lt_tok.

      IF lv_header = abap_false.
        IF lines( lt_tok ) < 4.
          rv_error = |line { lv_lineno } (the header) has fewer than owner, request type, target and step|.
          RETURN.
        ENDIF.
        FIND PCRE '^[A-Z]\z' IN lt_tok[ 2 ].
        IF sy-subrc <> 0.
          rv_error = |line { lv_lineno } (the header): request type '{ lt_tok[ 2 ] }' is not a single letter|.
          RETURN.
        ENDIF.
        FIND PCRE '^[A-Z0-9/_]{1,20}(\.[0-9]{3})?\z' IN lt_tok[ 3 ].
        IF sy-subrc <> 0.
          rv_error = |line { lv_lineno } (the header): target '{ lt_tok[ 3 ] }' is not a system|.
          RETURN.
        ENDIF.
        FIND PCRE '^[0-3]\z' IN lt_tok[ 4 ].
        IF sy-subrc <> 0.
          rv_error = |line { lv_lineno } (the header): step '{ lt_tok[ 4 ] }' is not one of 0, 1, 2, 3|.
          RETURN.
        ENDIF.
        " Nine object counts follow the step (what STRF_READ_COFILE reads);
        " what comes after them (release, flags, client) is not checked.
        LOOP AT lt_tok INTO DATA(lv_count) FROM 5 TO 13.
          FIND PCRE '^[0-9]+\z' IN lv_count.
          IF sy-subrc <> 0.
            rv_error = |line { lv_lineno } (the header): object count '{ lv_count }' is not a number|.
            RETURN.
          ENDIF.
        ENDLOOP.
        lv_header = abap_true.
        CONTINUE.
      ENDIF.

      IF lines( lt_tok ) < 4.
        rv_error = |line { lv_lineno }: a step line has at least system, function, return code and time|.
        RETURN.
      ENDIF.
      FIND PCRE '^[A-Z0-9/_]{1,20}(\.[0-9]{3})?\z' IN lt_tok[ 1 ].
      DATA(lv_ok) = xsdbool( sy-subrc = 0 ).
      FIND PCRE '^[0-9]+\z' IN lt_tok[ 3 ].
      lv_ok = xsdbool( lv_ok = abap_true AND sy-subrc = 0 ).
      FIND PCRE '^[0-9]{14}\z' IN lt_tok[ 4 ].
      lv_ok = xsdbool( lv_ok = abap_true AND sy-subrc = 0 AND strlen( lt_tok[ 2 ] ) = 1 ).
      IF lv_ok = abap_false.
        rv_error = |line { lv_lineno } is not a step line (<system>[.<client>] <function> <retcode> <YYYYMMDDhhmmss> ...)|.
        RETURN.
      ENDIF.
      lv_steps = lv_steps + 1.
      SPLIT lt_tok[ 1 ] AT '.' INTO lv_sys lv_cli.
      IF lv_sys = iv_sid AND lt_tok[ 2 ] = 'E'.
        lv_export = abap_true.
      ENDIF.
    ENDLOOP.

    IF lv_header = abap_false.
      rv_error = `the cofile has no header line`.
    ELSEIF lv_steps = 0.
      rv_error = `the cofile records no steps: the request was never exported`.
    ELSEIF lv_export = abap_false.
      rv_error = |the cofile records no export (step E) from { iv_sid }, the system its name says it comes from|.
    ENDIF.
  ENDMETHOD.


  METHOD dir_of.
    " The only two directories this class touches, both inside DIR_TRANS.
    rv_dir = COND #( WHEN iv_kind = `cofile` THEN '$TR_COFI' ELSE '$TR_DATA' ).
  ENDMETHOD.


  METHOD file_exists.
    rv_exists = probe_file( EXPORTING iv_subdir = CONV #( dir_of( iv_kind ) ) iv_name = iv_name
                            IMPORTING ev_error = ev_error ).
  ENDMETHOD.


  METHOD probe_file.
    DATA: lv_name    TYPE eps2filnam,
          lv_dirname TYPE eps2filnam,
          lv_mask    TYPE epsf-epsfilnam,
          lt_list    TYPE STANDARD TABLE OF eps2fili WITH DEFAULT KEY.

    CLEAR: ev_error, ev_long_dir.
    lv_name = iv_name.
    CALL FUNCTION 'EPS_GET_DIRECTORY_PATH'
      EXPORTING
        eps_subdir       = iv_subdir
      IMPORTING
        ev_long_dir_name = ev_long_dir
      EXCEPTIONS
        OTHERS           = 1.
    IF sy-subrc <> 0 OR ev_long_dir IS INITIAL.
      ev_error = |DIR_TRANS ({ iv_subdir }) could not be resolved: { last_message( ) }|.
      RETURN.
    ENDIF.
    CALL FUNCTION 'EPS_GET_FILE_ATTRIBUTES'
      EXPORTING
        iv_long_file_name      = lv_name
        iv_long_dir_name       = ev_long_dir
      EXCEPTIONS
        read_directory_failed  = 1
        read_attributes_failed = 2
        OTHERS                 = 3.
    IF sy-subrc = 0.
      rv_exists = abap_true.
      RETURN.
    ENDIF.

    " No attributes: missing, or unreadable? Only a listing of the
    " directory without the file says missing.
    lv_dirname = ev_long_dir.
    lv_mask = iv_name.
    CALL FUNCTION 'EPS2_GET_DIRECTORY_LISTING'
      EXPORTING
        iv_dir_name            = lv_dirname
        file_mask              = lv_mask
      TABLES
        dir_list               = lt_list
      EXCEPTIONS
        invalid_eps_subdir     = 1
        sapgparam_failed       = 2
        build_directory_failed = 3
        no_authorization       = 4
        read_directory_failed  = 5
        too_many_read_errors   = 6
        empty_directory_list   = 7
        OTHERS                 = 8.
    DATA(lv_subrc) = sy-subrc.
    IF lv_subrc = 7 OR ( lv_subrc = 0 AND NOT line_exists( lt_list[ name = lv_name ] ) ).
      rv_exists = abap_false.
    ELSEIF lv_subrc = 0.
      ev_error = |{ iv_name } is listed in { ev_long_dir } but its attributes cannot be read|.
    ELSE.
      ev_error = |{ ev_long_dir } cannot be listed (exception { lv_subrc }) { last_message( ) }|.
    ENDIF.
  ENDMETHOD.


  METHOD write_file.
    DATA: lv_dir      TYPE epsf-epsdirnam,
          lv_name     TYPE eps2filnam,
          lv_long_dir TYPE eps2path,
          lv_path     TYPE eps2path,
          lv_size     TYPE epsf-epsfilsiz,
          lt_buf      TYPE STANDARD TABLE OF tbl8000 WITH DEFAULT KEY,
          ls_buf      TYPE tbl8000,
          lv_off      TYPE i,
          lv_n        TYPE i,
          lv_last     TYPE i,
          lv_records  TYPE i.

    CLEAR: ev_opened, ev_path, ev_error.
    lv_dir = dir_of( iv_kind ).
    lv_name = iv_name.

    " overwrite_mode space: SAP refuses a non-empty existing file; the caller
    " has already refused any existing file, empty or not.
    TRY.
        CALL FUNCTION 'EPS_OPEN_OUTPUT_FILE'
          EXPORTING
            iv_long_file_name      = lv_name
            dir_name               = lv_dir
            overwrite_mode         = space
          IMPORTING
            ev_long_dir_name       = lv_long_dir
            ev_long_file_path      = lv_path
          EXCEPTIONS
            invalid_eps_subdir     = 1
            sapgparam_failed       = 2
            build_directory_failed = 3
            no_authorization       = 4
            build_path_failed      = 5
            open_failed            = 6
            file_already_exists    = 7
            OTHERS                 = 8.
        DATA(lv_subrc) = sy-subrc.
      CATCH cx_root INTO DATA(lx_open).
        ev_error = |open failed: { lx_open->get_text( ) }|.
        RETURN.
    ENDTRY.
    IF lv_subrc <> 0.
      ev_error = SWITCH #( lv_subrc
        WHEN 4 THEN |no authorization (S_CTS_ADMI CTS_ADMFCT EPS1, or S_DATASET for the transport directory) { last_message( ) }|
        WHEN 7 THEN `the file already exists`
        WHEN 6 THEN |the file could not be opened for writing in DIR_TRANS ({ lv_dir }): does the directory exist, writable by the SAP system's OS user? { last_message( ) }|
        ELSE |EPS_OPEN_OUTPUT_FILE failed (exception { lv_subrc }) { last_message( ) }| ).
      RETURN.
    ENDIF.
    ev_opened = abap_true.
    ev_path = lv_path.

    DATA(lv_len) = xstrlen( iv_content ).
    WHILE lv_off < lv_len.
      lv_n = nmin( val1 = 8000 val2 = lv_len - lv_off ).
      CLEAR ls_buf.
      ls_buf-line = iv_content+lv_off(lv_n).
      APPEND ls_buf TO lt_buf.
      lv_last = lv_n.
      lv_off = lv_off + lv_n.
    ENDWHILE.
    lv_records = lines( lt_buf ).

    TRY.
        CALL FUNCTION 'EPS_WRITE_BLOCK'
          EXPORTING
            iv_long_file_path  = lv_path
            number_of_records  = lv_records
            last_record_length = lv_last
          TABLES
            eps_buffer         = lt_buf
          EXCEPTIONS
            write_failure      = 1
            OTHERS             = 2.
        lv_subrc = sy-subrc.
      CATCH cx_root INTO DATA(lx_write).
        lv_subrc = 9.
        ev_error = |write failed: { lx_write->get_text( ) }|.
    ENDTRY.

    CALL FUNCTION 'EPS_CLOSE_FILE'
      EXPORTING
        iv_long_file_name      = lv_name
        iv_long_dir_name       = lv_long_dir
      IMPORTING
        file_size              = lv_size
      EXCEPTIONS
        build_path_failed      = 1
        read_directory_failed  = 2
        read_attributes_failed = 3
        OTHERS                 = 4.
    DATA(lv_close_subrc) = sy-subrc.

    IF lv_subrc <> 0.
      IF ev_error IS INITIAL.
        ev_error = |EPS_WRITE_BLOCK failed (exception { lv_subrc })|.
      ENDIF.
      RETURN.
    ENDIF.
    IF lv_close_subrc <> 0.
      ev_error = |EPS_CLOSE_FILE failed (exception { lv_close_subrc })|.
      RETURN.
    ENDIF.
    IF lv_size <> lv_len.
      ev_error = |{ lv_size } bytes on disk after writing { lv_len }|.
      RETURN.
    ENDIF.
  ENDMETHOD.


  METHOD delete_file.
    DATA: lv_dir  TYPE epsf-epsdirnam,
          lv_name TYPE eps2filnam.

    lv_dir = dir_of( iv_kind ).
    lv_name = iv_name.
    CALL FUNCTION 'EPS_DELETE_FILE'
      EXPORTING
        iv_long_file_name = lv_name
        dir_name          = lv_dir
      EXCEPTIONS
        OTHERS            = 1.
  ENDMETHOD.


  METHOD read_buffer.
    DATA: lv_cmd    TYPE stpa-cmdstring,
          lv_rc     TYPE stpa-retcode,
          lv_msg    TYPE stpa-message,
          lv_system TYPE stpa-sysname.

    CLEAR: et_buffer, et_stdout, ev_cmd, ev_rc, ev_msg, ev_error.
    lv_system = sy-sysid.
    " Read-only: no lock clearing, no lock reading, nothing but the list.
    CALL FUNCTION 'TMS_TP_SHOW_BUFFER'
      EXPORTING
        iv_system_name     = lv_system
      IMPORTING
        ev_tp_cmd_strg     = lv_cmd
        ev_tp_ret_code     = lv_rc
        ev_tp_message      = lv_msg
      TABLES
        tt_stdout          = et_stdout
        tt_buffer          = et_buffer
      EXCEPTIONS
        permission_denied  = 1
        tp_call_failed     = 2
        tp_interface_error = 3
        tp_reported_error  = 4
        OTHERS             = 5.
    DATA(lv_subrc) = sy-subrc.
    ev_cmd = lv_cmd.
    ev_rc = lv_rc.
    ev_msg = lv_msg.
    IF lv_subrc <> 0.
      ev_error = |TMS_TP_SHOW_BUFFER failed (exception { lv_subrc }, tp rc { lv_rc }): { last_message( ) } { lv_msg }|.
    ENDIF.
  ENDMETHOD.


  METHOD request_parts.
    CLEAR: ev_sid, ev_number.
    FIND PCRE '^([A-Z0-9]{3})K([0-9]{6})\z' IN iv_request SUBMATCHES ev_sid ev_number.
    rv_ok = xsdbool( sy-subrc = 0 ).
  ENDMETHOD.


  METHOD sha256.
    TRY.
        cl_abap_message_digest=>calculate_hash_for_raw(
          EXPORTING if_algorithm  = 'SHA256'
                    if_data       = iv_data
          IMPORTING ef_hashstring = DATA(lv_hash) ).
        rv_hash = to_upper( lv_hash ).
      CATCH cx_abap_message_digest.
        CLEAR rv_hash.
    ENDTRY.
  ENDMETHOD.


  METHOD last_message.
    IF sy-msgid IS INITIAL.
      RETURN.
    ENDIF.
    MESSAGE ID sy-msgid TYPE 'S' NUMBER sy-msgno
      WITH sy-msgv1 sy-msgv2 sy-msgv3 sy-msgv4 INTO rv_text.
  ENDMETHOD.


  METHOD stdout_json.
    DATA lt_items TYPE string_table.
    LOOP AT it_stdout INTO DATA(ls_line).
      APPEND |"{ zcl_vsp_utils=>escape_json( CONV #( ls_line-line ) ) }"| TO lt_items.
    ENDLOOP.
    rv_json = zcl_vsp_utils=>json_arr( zcl_vsp_utils=>json_join( lt_items ) ).
  ENDMETHOD.


  METHOD err.
    rs_response = zcl_vsp_utils=>build_error( iv_id = iv_id iv_code = iv_code iv_message = iv_message ).
  ENDMETHOD.

ENDCLASS.
