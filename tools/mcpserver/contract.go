package mcpserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/contract"
)

// contractFileName is the well-known repository contract file name, per
// docs/planning/agent-repository-contract-guide.md.
const contractFileName = "modulex.agent.yaml"

// ReadContractIn is read_contract's input.
type ReadContractIn struct {
	// Root is the repository root expected to contain modulex.agent.yaml.
	// Defaults to "." if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root containing modulex.agent.yaml; defaults to \".\" if empty"`
}

// ReadContractOut is read_contract's output. Present, Contract, and
// ValidationErrors together form a tri-state result — see readContract's
// doc comment for what each combination means.
type ReadContractOut struct {
	// Present is false if root has no modulex.agent.yaml at all. This is a
	// normal outcome, not an error: most repositories have no contract yet.
	Present bool `json:"present"`
	// Contract is the parsed contract, non-nil only when the YAML parsed
	// successfully (regardless of whether it then passed Validate).
	Contract *contract.Contract `json:"contract,omitempty"`
	// ValidationErrors is non-empty when Present is true but the file
	// either failed to parse as YAML (one generic parse-error string,
	// Contract nil) or parsed but failed contract.Contract.Validate (one
	// string per validation error, Contract non-nil).
	ValidationErrors []string `json:"validation_errors,omitempty"`
}

// readContract reads <root>/modulex.agent.yaml, if present, and validates
// it. Three outcomes, distinguished in the returned ReadContractOut rather
// than by error, since all three are normal, expected results a caller
// should handle by inspecting the response, not by error-handling:
//
//   - No file at all: {Present: false}.
//   - File present but not valid YAML: {Present: true, Contract: nil,
//     ValidationErrors: [parse error]}.
//   - File present, valid YAML, but fails contract.Contract.Validate:
//     {Present: true, Contract: <parsed>, ValidationErrors: [...]}.
//   - File present and fully valid: {Present: true, Contract: <parsed>,
//     ValidationErrors: nil}.
//
// A real error is returned only for a genuine I/O failure other than "file
// does not exist" (e.g. a permission error) — not a normal outcome for this
// tool to model in its output.
func readContract(root string) (ReadContractOut, error) {
	path := filepath.Join(resolveRoot(root), contractFileName)

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ReadContractOut{Present: false}, nil
	}
	if err != nil {
		return ReadContractOut{}, err
	}

	var c contract.Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return ReadContractOut{
			Present:          true,
			ValidationErrors: []string{"parsing " + contractFileName + ": " + err.Error()},
		}, nil
	}

	if err := c.Validate(); err != nil {
		return ReadContractOut{
			Present:          true,
			Contract:         &c,
			ValidationErrors: unwrapErrors(err),
		}, nil
	}

	return ReadContractOut{Present: true, Contract: &c}, nil
}

// unwrapErrors flattens an error tree built by errors.Join (as
// contract.Contract.Validate returns) into one string per leaf error.
// errors.Join's result implements Unwrap() []error (Go 1.20+); falling
// back to a single-element slice for any other error shape keeps this safe
// to call on an arbitrary error, not just one from errors.Join.
func unwrapErrors(err error) []string {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs := joined.Unwrap()
		out := make([]string, len(errs))
		for i, e := range errs {
			out[i] = e.Error()
		}
		return out
	}
	return []string{err.Error()}
}

func readContractHandler(_ context.Context, _ *mcp.CallToolRequest, in ReadContractIn) (*mcp.CallToolResult, ReadContractOut, error) {
	out, err := readContract(in.Root)
	return nil, out, err
}
