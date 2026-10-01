*&---------------------------------------------------------------------*
*& Report ZVSP_TRANSPORT_BUFFER
*&---------------------------------------------------------------------*
*& The background step of ZCL_VSP_TRANSPORT_SERVICE. tp is started over
*& synchronous RFC, which an ABAP Push Channel may not do, so the service
*& schedules this report as job ZVSP_TRANSPORT_BUFFER, binding the request
*& and the SHA-256 of its two files to the step. Run outside that job it
*& does nothing; in it, it adds the request only if both files still have
*& those SHA-256 values.
*&---------------------------------------------------------------------*
REPORT zvsp_transport_buffer.

PARAMETERS: p_req  TYPE trkorr NO-DISPLAY,
            p_shac TYPE c LENGTH 64 NO-DISPLAY,
            p_shad TYPE c LENGTH 64 NO-DISPLAY.

START-OF-SELECTION.
  zcl_vsp_transport_service=>run_job( iv_request = p_req iv_cofile_sha = p_shac iv_data_sha = p_shad ).
