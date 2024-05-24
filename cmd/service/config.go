package main

import (
	"emperror.dev/errors"
	"github.com/BurntSushi/toml"
	"github.com/je4/filesystem/v2/pkg/vfsrw"
	"github.com/je4/trustutil/v2/pkg/loader"
	"github.com/je4/utils/v2/pkg/config"
	"io/fs"
	"os"
)

type MediaserverMainConfig struct {
	LocalAddr               string                `toml:"localaddr"`
	ExternalAddr            string                `toml:"externaladdr"`
	ResolverAddr            string                `toml:"resolveraddr"`
	ResolverTimeout         config.Duration       `toml:"resolvertimeout"`
	ResolverNotFoundTimeout config.Duration       `toml:"resolvernotfoundtimeout"`
	WebTLS                  loader.TLSConfig      `toml:"webtls"`
	ServerTLS               *loader.TLSConfig     `toml:"server"`
	ClientTLS               *loader.TLSConfig     `toml:"client"`
	LogFile                 string                `toml:"logfile"`
	LogLevel                string                `toml:"loglevel"`
	GRPCClient              map[string]string     `toml:"grpcclient"`
	VFS                     map[string]*vfsrw.VFS `toml:"vfs"`
}

func LoadMediaserverMainConfig(fSys fs.FS, fp string, conf *MediaserverMainConfig) error {
	if _, err := fs.Stat(fSys, fp); err != nil {
		path, err := os.Getwd()
		if err != nil {
			return errors.Wrap(err, "cannot get current working directory")
		}
		fSys = os.DirFS(path)
		fp = "mediaservermain.toml"
	}
	data, err := fs.ReadFile(fSys, fp)
	if err != nil {
		return errors.Wrapf(err, "cannot read file [%v] %s", fSys, fp)
	}
	_, err = toml.Decode(string(data), conf)
	if err != nil {
		return errors.Wrapf(err, "error loading config file %v", fp)
	}
	return nil
}
