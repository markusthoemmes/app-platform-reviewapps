package main

import (
	"fmt"
	"os"

	"github.com/palantir/go-githubapp/githubapp"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Server       HTTPConfig         `yaml:"server"`
	Github       githubapp.Config   `yaml:"github"`
	DigitalOcean DigitalOceanConfig `yaml:"do"`
}

type HTTPConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type DigitalOceanConfig struct {
	Token string `yaml:"token"`
}

func ReadConfig(path string) (*Config, error) {
	var c Config

	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed reading server config file: %s: %w", path, err)
	}

	if err := yaml.UnmarshalStrict(bytes, &c); err != nil {
		return nil, fmt.Errorf("failed parsing configuration file: %w", err)
	}

	return &c, nil
}
