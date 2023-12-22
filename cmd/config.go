package cmd

import (
	"strconv"
	"time"

	"github.com/steviebps/realm/utils"
)

type RealmConfig struct {
	Client ClientConfig `json:"client,omitempty"`
	Server ServerConfig `json:"server,omitempty"`
}

func NewDefaultServerConfig() RealmConfig {
	return RealmConfig{
		Client: ClientConfig{},
		Server: ServerConfig{StorageType: "bigcache", StorageOptions: map[string]string{"life_window": strconv.FormatInt(int64(time.Hour*24), 10)}, Port: "8080", Inheritable: true},
	}
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
