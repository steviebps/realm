package options

import (
	"os"

	"github.com/spf13/viper"
	rein "github.com/steviebps/rein/pkg"
	"github.com/steviebps/rein/utils"
)

const saveAndExit = "Save & Exit"
const exit = "Exit Without Saving"

func NewSaveAndExit(associated *rein.Chamber, displayed *rein.Chamber) OpenOption {
	action := func(asssociated *rein.Chamber) {
		chamberFile := viper.GetString("chamber")
		utils.WriteChamberToFile(chamberFile, *asssociated, true)
		os.Exit(0)
	}

	return New(saveAndExit, associated, displayed, action)
}

func NewExit(displayed *rein.Chamber) OpenOption {
	action := func(*rein.Chamber) {
		os.Exit(0)
	}

	return New(exit, nil, displayed, action)
}
