package nativeauth

// Fixed failure vocabulary only. No upstream body, key/value, URL or SDK text.
var ADIResponseCodes = []string{
	"native_adi_body_read_failed", "native_adi_body_too_large", "native_adi_body_empty",
	"native_adi_plist_invalid_xml", "native_adi_plist_envelope_invalid", "native_adi_plist_duplicate_key",
	"native_adi_plist_type_unsupported", "native_adi_plist_real_unsupported", "native_adi_plist_date_unsupported",
	"native_adi_plist_integer_invalid", "native_adi_plist_integer_range", "native_adi_plist_data_invalid",
	"native_adi_plist_real_invalid", "native_adi_plist_depth_limit", "native_adi_plist_node_limit",
	"native_adi_plist_binary_unsupported", "native_adi_status_invalid", "native_adi_service_rejected",
	"native_adi_spim_invalid", "native_adi_cpim_invalid", "native_adi_ptm_invalid",
	"native_adi_trust_key_invalid", "native_adi_routing_invalid", "native_adi_endpoints_invalid",
}
