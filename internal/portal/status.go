package portal

type Overall string

const (
	OverallOK      Overall = "ok"
	OverallWarn    Overall = "warn"
	OverallError   Overall = "error"
	OverallUnknown Overall = "unknown"
)

type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

type Diagnostic struct {
	Component  string   `json:"component"`
	Severity   Severity `json:"severity"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Evidence   string   `json:"evidence,omitempty"`
	NextAction string   `json:"next_action,omitempty"`
}

type Status struct {
	SpaceID     string         `json:"space_id"`
	PortalID    string         `json:"portal_id"`
	Driver      Driver         `json:"driver"`
	Overall     Overall        `json:"overall"`
	State       string         `json:"state"`
	Health      string         `json:"health"`
	Auth        []AuthProvider `json:"auth"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
}

type ListEntry struct {
	SpaceID  string   `json:"space_id"`
	PortalID string   `json:"portal_id"`
	Driver   Driver   `json:"driver"`
	SyncMode SyncMode `json:"sync_mode"`
	Auth     string   `json:"auth"`
	Notes    string   `json:"notes,omitempty"`
}

type DriverInfo struct {
	Driver        Driver `json:"driver"`
	CreateCapable bool   `json:"create_capable"`
	AttachOnly    bool   `json:"attach_only"`
	Binary        string `json:"binary"`
	Description   string `json:"description"`
}

type DoctorReport struct {
	SpaceID     string       `json:"space_id"`
	PortalID    string       `json:"portal_id"`
	Overall     Overall      `json:"overall"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type InspectReport struct {
	ManifestPath       string   `json:"manifest_path"`
	Portal             Portal   `json:"portal"`
	OwnedResources     []string `json:"owned_resources"`
	SyncScope          []string `json:"sync_scope"`
	DestroyDryRunNotes []string `json:"destroy_dry_run_notes"`
}
