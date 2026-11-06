package cmd

import (
	"testing"

	"github.com/spf13/pflag"
)

func TestTracesMode(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		want    string
		wantErr bool
	}{
		{name: "off by default", want: tracesOff},
		{name: "otlp flag", args: []string{"--traces=otlp"}, want: tracesOTLP},
		{name: "stdout flag", args: []string{"--traces", "stdout"}, want: tracesStdout},
		{name: "flag is case-insensitive", args: []string{"--traces=OTLP"}, want: tracesOTLP},
		{name: "env", env: "otlp", want: tracesOTLP},
		{name: "flag beats env", args: []string{"--traces=off"}, env: "otlp", want: tracesOff},
		{name: "deprecated notraces", args: []string{"--notraces"}, env: "otlp", want: tracesOff},
		{name: "deprecated stdouttraces", args: []string{"--stdouttraces"}, want: tracesStdout},
		{name: "traces beats deprecated flags", args: []string{"--stdouttraces", "--traces=otlp"}, want: tracesOTLP},
		{name: "invalid flag", args: []string{"--traces=jaeger"}, wantErr: true},
		{name: "invalid env", env: "on", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(TracesEnv, tt.env)
			flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
			addTracesFlags(flags)
			if err := flags.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			got, err := tracesMode(flags)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
