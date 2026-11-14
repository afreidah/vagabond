// -------------------------------------------------------------------------------
// Code Engine Configuration
//
// Author: Alex Freidah
//
// The values a deployment has to state, decoded from the provider's own config
// block. Vagabond hands that block over undecoded, so a typo is reported
// against the line the operator wrote it on rather than at dispatch. A wrong
// region or project surfaces from Code Engine as 403, not 404, so both are
// checked here as far as they can be.
// -------------------------------------------------------------------------------

package ibm

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Type is what a provider block names to select this plugin.
const Type = "code-engine"

// apiVersion is the date every v2 call pins its behaviour to.
const apiVersion = "2026-09-04"

// regions are where Code Engine runs. One outside the list is warned about, not
// refused, so a region IBM adds later still works.
var regions = []string{
	"au-syd", "br-sao", "ca-tor", "eu-de", "eu-gb",
	"jp-osa", "jp-tok", "us-east", "us-south",
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is what Code Engine needs beyond the API key. ImageSecret names a
// registry secret already in the project, for images that need a login.
type Config struct {
	Region      string  `hcl:"region"`
	ProjectID   string  `hcl:"project_id"`
	ImageSecret *string `hcl:"image_secret,optional"`
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// decodeConfig reads the provider's own block.
func decodeConfig(name string, body hcl.Body) (*Config, hcl.Diagnostics) {
	cfg, diags := plugin.DecodeConfig[Config](name, Type, body, "region and project_id")
	if cfg == nil {
		return nil, diags
	}

	return cfg, append(diags, cfg.validate(name)...)
}

// validate reports configuration that decoded but cannot be used, and warns
// about a region Code Engine is not known to run in.
func (c *Config) validate(name string) hcl.Diagnostics {
	settings := map[string]string{"region": c.Region, "project_id": c.ProjectID}
	if c.ImageSecret != nil {
		settings["image_secret"] = *c.ImageSecret
	}

	diags := plugin.EmptySettings(name, settings)

	if c.ProjectID != "" {
		if _, err := uuid.Parse(c.ProjectID); err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid project_id",
				Detail: fmt.Sprintf("Provider %q sets project_id to %q, which is not a "+
					"project ID. Code Engine project IDs are UUIDs: "+
					"ibmcloud ce project get --name <project> prints it.", name, c.ProjectID),
			})
		}
	}

	if c.Region != "" && !slices.Contains(regions, c.Region) {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagWarning,
			Summary:  "Unknown Code Engine region",
			Detail: fmt.Sprintf("Provider %q sets region to %q, which is not one of %s. "+
				"A wrong region fails every call with 403.",
				name, c.Region, strings.Join(regions, ", ")),
		})
	}

	return diags
}

// -------------------------------------------------------------------------
// ENDPOINTS
// -------------------------------------------------------------------------

// apiBase is the region's API host, serving both the v2 API and the v1
// kubeconfig call.
func apiBase(region string) string {
	return "https://api." + region + ".codeengine.cloud.ibm.com"
}

// jobRunsURL is the collection every job run of this provider lives in, built
// from a base the provider holds so a test can point it at an httptest server.
func (c *Config) jobRunsURL(base string) string {
	return fmt.Sprintf("%s/v2/projects/%s/job_runs?version=%s", base, c.ProjectID, apiVersion)
}

// jobRunURL addresses one job run by name.
func (c *Config) jobRunURL(base, name string) string {
	return fmt.Sprintf("%s/v2/projects/%s/job_runs/%s?version=%s",
		base, c.ProjectID, name, apiVersion)
}

// kubeconfigURL is the v1 call that names the project's Kubernetes namespace,
// which nothing in the v2 API reports.
func (c *Config) kubeconfigURL(base string) string {
	return base + "/api/v1/project/" + c.ProjectID + "/config"
}
