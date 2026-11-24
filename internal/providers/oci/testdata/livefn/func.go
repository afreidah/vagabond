// -------------------------------------------------------------------------------
// Live Test Function
//
// Author: Alex Freidah
//
// The function the live tests invoke, deployed to OCI Functions from this
// directory. It replies under Vagabond's function contract with exit code 3 and
// an output naming the execution, its args and one env value, or sleeps past
// its 30 s timeout when its first arg is "sleep".
// -------------------------------------------------------------------------------

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	fdk "github.com/fnproject/fdk-go"
)

// main serves invokes on the socket OCI Functions names.
func main() {
	fdk.Handle(fdk.HandlerFunc(reply))
}

// reply answers one event with exit code 3 and what it was sent, so a reply
// that lost the code or the event's contents is caught.
func reply(_ context.Context, in io.Reader, out io.Writer) {
	var event struct {
		ExecutionID string            `json:"execution_id"`
		Args        []string          `json:"args"`
		Env         map[string]string `json:"env"`
	}

	_ = json.NewDecoder(in).Decode(&event)

	if len(event.Args) > 0 && event.Args[0] == "sleep" {
		time.Sleep(time.Minute)
	}

	_ = json.NewEncoder(out).Encode(map[string]any{
		"exit_code": 3,
		"output":    fmt.Sprintf("hello %s [%s] %s\n", event.ExecutionID, strings.Join(event.Args, "|"), event.Env["VAGABOND_LIVE"]),
	})
}
