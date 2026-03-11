package models

// AvailabilityStatus represents Libby availability for a book at a library.
type AvailabilityStatus string

const (
	StatusAvailable   AvailabilityStatus = "available"
	StatusWait        AvailabilityStatus = "wait"
	StatusUnavailable AvailabilityStatus = "unavailable"
	StatusNotFound    AvailabilityStatus = "not_found"
)

// LibraryResult is the OverDrive availability result for one book at one library.
type LibraryResult struct {
	LibraryKey      string             `json:"library_key"`
	LibraryName     string             `json:"library_name"`
	Status          AvailabilityStatus `json:"status"`
	AvailableCopies int                `json:"available_copies"`
	OwnedCopies     int                `json:"owned_copies"`
	HoldsCount      int                `json:"holds_count"`
	EstimatedWait   int                `json:"estimated_wait_days"`
	OverDriveURL    string             `json:"overdrive_url,omitempty"`
	HasKindle       bool               `json:"has_kindle,omitempty"` // true if the Libby/OverDrive title includes a Kindle delivery format
}

// Library is a row from the libraries directory table.
type Library struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Website string `json:"website"`
}
