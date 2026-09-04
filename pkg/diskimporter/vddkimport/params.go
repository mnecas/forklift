package vddkimport

// Params holds VDDK warm-migration import parameters matching CDI's importer env contract.
type Params struct {
	Endpoint           string
	AccessKey          string
	SecretKey          string
	Thumbprint         string
	UUID               string
	BackingFile        string
	CurrentCheckpoint  string
	PreviousCheckpoint string
	FinalCheckpoint    bool
	ImageSize          string
	Preallocation      bool
	FilesystemOverhead float64
	CertDir            string
	InsecureTLS        bool
}
