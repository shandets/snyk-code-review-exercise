package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/snyk/snyk-code-review-exercise/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackageHandler(t *testing.T) {
	tests := []struct {
		name        string
		getPath     string
		fixtureFile string
		packageName string
		packageVer  string
		statusCod   int
	}{
		{
			name:        "React v16.13.0",
			getPath:     "/package/react/16.13.0",
			fixtureFile: "react-16.13.0.json",
			packageName: "react",
			packageVer:  "16.13.0",
			statusCod:   http.StatusOK,
		}, {
			name:        "Bogus React v16.13.0",
			getPath:     "/package/bogusreact/16.13.0",
			fixtureFile: "wrong-package-404.json",
			packageName: "bogusreact",
			packageVer:  "16.13.0",
			statusCod:   http.StatusNotFound,
		}, {
			name:        "React Bogus Version v16.13.0.bogus",
			getPath:     "/package/bogusreact/16.13.0.bogus",
			fixtureFile: "wrong-version-404.json",
			packageName: "bogusreact",
			packageVer:  "16.13.0.bogus",
			statusCod:   http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up the HTTP test server
			handler := api.New()
			server := httptest.NewServer(handler)
			defer server.Close()

			// Make the GET request to the specified path
			resp, err := server.Client().Get(server.URL + tt.getPath)
			require.Nil(t, err)
			defer resp.Body.Close()

			// Validate the status code
			assert.Equal(t, tt.statusCod, resp.StatusCode)

			// Read the response body
			body, err := io.ReadAll(resp.Body)
			require.Nil(t, err)

			// Unmarshal the response into a NpmPackageVersion object
			var data api.NpmPackageVersion
			err = json.Unmarshal(body, &data)
			require.Nil(t, err)

			if tt.statusCod == http.StatusOK {
				// Assert that the package name and version are as expected
				assert.Equal(t, tt.packageName, data.Name)
				assert.Equal(t, tt.packageVer, data.Version)
			}

			// Load the fixture file and compare it with the response
			fixture, err := os.Open(filepath.Join("testdata", tt.fixtureFile))
			require.Nil(t, err)
			defer fixture.Close() // Ensure the file is closed after the test

			var fixtureObj api.NpmPackageVersion
			require.Nil(t, json.NewDecoder(fixture).Decode(&fixtureObj))

			// Assert that the response matches the contents of the fixture
			assert.Equal(t, fixtureObj, data)
		})
	}
}
