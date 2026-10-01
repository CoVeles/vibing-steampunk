*&---------------------------------------------------------------------*
*& Report ZVSP_TRANSPORT_BUFFER
*&---------------------------------------------------------------------*
*& The background step of ZCL_VSP_TRANSPORT_SERVICE. tp is started over
*& synchronous RFC, which an ABAP Push Channel may not do, so the service
*& schedules this report as job ZVSP_TRANSPORT_BUFFER and collects the
*& result. It takes no parameters: run outside that job, or without the
*& ticket the service stored for the job, it does nothing.
*&---------------------------------------------------------------------*
REPORT zvsp_transport_buffer.

START-OF-SELECTION.
  zcl_vsp_transport_service=>run_job( ).
