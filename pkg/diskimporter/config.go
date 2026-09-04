package diskimporter

import (
	"fmt"
	"os"
	"strconv"
)

const (
	defaultMountPath           = "/data"
	diskImageName              = "disk.img"
	defaultFilesystemOverhead  = 0.055
	defaultPreallocation       = false
)

// Config holds runtime parameters for a single checkpoint import.
type Config struct {
	TransferType       string
	MountPath          string
	TargetImage        string
	CheckpointCurrent  string
	CheckpointPrevious string
	FinalCheckpoint    bool
	ImageSize          string
	Preallocation      bool
	FilesystemOverhead float64
	CertDir            string
	InsecureTLS        bool
	VDDK               VDDKConfig
}

// VDDKConfig holds VDDK transfer parameters.
type VDDKConfig struct {
	URL          string
	UUID         string
	BackingFile  string
	Thumbprint   string
	SecretRef    string
	InitImageURL string
	AccessKey    string
	SecretKey    string
}

// LoadConfigFromEnv reads importer configuration from the process environment.
func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		TransferType:       os.Getenv("TRANSFER_TYPE"),
		MountPath:          envOr("IMPORTER_MOUNT_PATH", defaultMountPath),
		CheckpointCurrent:  os.Getenv("CHECKPOINT_CURRENT"),
		CheckpointPrevious: os.Getenv("CHECKPOINT_PREVIOUS"),
		FinalCheckpoint:    envBool("FINAL_CHECKPOINT"),
		ImageSize:          os.Getenv("IMPORTER_IMAGE_SIZE"),
		Preallocation:      envBoolDefault("PREALLOCATION", defaultPreallocation),
		FilesystemOverhead: envFloatDefault("FILESYSTEM_OVERHEAD", defaultFilesystemOverhead),
		CertDir:            os.Getenv("IMPORTER_CERT_DIR"),
		InsecureTLS:        envBool("INSECURE_TLS"),
		VDDK: VDDKConfig{
			URL:          os.Getenv("VDDK_URL"),
			UUID:         os.Getenv("VDDK_UUID"),
			BackingFile:  os.Getenv("VDDK_BACKING_FILE"),
			Thumbprint:   os.Getenv("VDDK_THUMBPRINT"),
			SecretRef:    os.Getenv("VDDK_SECRET_REF"),
			InitImageURL: os.Getenv("VDDK_INIT_IMAGE_URL"),
			AccessKey:    os.Getenv("IMPORTER_ACCESS_KEY_ID"),
			SecretKey:    os.Getenv("IMPORTER_SECRET_KEY"),
		},
	}
	cfg.TargetImage = fmt.Sprintf("%s/%s", cfg.MountPath, diskImageName)
	if cfg.TransferType == "" {
		return cfg, fmt.Errorf("TRANSFER_TYPE is required")
	}
	if cfg.CheckpointCurrent == "" {
		return cfg, fmt.Errorf("CHECKPOINT_CURRENT is required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func envBool(key string) bool {
	val, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && val
}

func envBoolDefault(key string, fallback bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	val, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return val
}

func envFloatDefault(key string, fallback float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return val
}
