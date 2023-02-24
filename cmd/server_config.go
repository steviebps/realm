package cmd

type ServerConfig struct {
	StorageType    string            `json:"storage"`
	StorageOptions map[string]string `json:"options"`
	Port           string            `json:"port"`
	CertFile       string            `json:"certFile"`
	KeyFile        string            `json:"keyFile"`
	LogLevel       string            `json:"logLevel"`
}
