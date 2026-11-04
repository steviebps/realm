package cmd

import (
	"strconv"
	"time"

	"github.com/steviebps/realm/auth"
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
	// LogLevel is the minimum level logged: debug, info, warn, or error.
	LogLevel    string `json:"logLevel"`
	Inheritable bool   `json:"inheritable"`
	// ShutdownDelay is how long the server keeps serving after a shutdown
	// signal while GET /readyz reports 503, so load balancers stop sending it
	// traffic first (a duration such as "5s"; default none).
	ShutdownDelay string `json:"shutdownDelay,omitempty"`
	// Auth enables authentication; without it the server is open to anyone
	// who can reach it.
	Auth *auth.Config `json:"auth,omitempty"`
}

type ClientConfig struct {
	Address string `json:"address"`
	// Token is an API key; it may reference an environment variable as ${NAME}.
	Token string `json:"token,omitempty"`
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
