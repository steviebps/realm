package cmd

import (
	"github.com/steviebps/realm/utils"
)

type RealmConfig struct {
	Client ClientConfig `json:"client,omitempty"`
	Server ServerConfig `json:"server,omitempty"`
}

type ServerConfig struct {
	StorageType    string            `json:"storage"`
	StorageOptions map[string]string `json:"options"`
	Port           string            `json:"port"`
	CertFile       string            `json:"certFile"`
	KeyFile        string            `json:"keyFile"`
	LogLevel       string            `json:"logLevel"`
	Inheritable    bool              `json:"inheritable"`
}

type ClientConfig struct {
	Address string `json:"address"`
}

func parseConfig(path string) (RealmConfig, error) {
	var config RealmConfig

	file, err := utils.OpenFile(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	err = utils.ReadInterfaceWith(file, &config)
	return config, err
}
