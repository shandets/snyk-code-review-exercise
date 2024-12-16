package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/gorilla/mux"
)

// review: add custom error type to return errors from the npm registry endpoint.
type HTTPError struct {
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("status %d: %s", e.StatusCode, e.Message)
}

func NewHTTPError(statusCode int, message string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Message:    message,
	}
}

// review: render error as json in the response
func sendJSONError(w http.ResponseWriter, err *HTTPError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.StatusCode)
	jsonResponse, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		// review: fallback to plain text error if json encoding fails
		http.Error(w, `{"status_code":500,"message":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	w.Write(jsonResponse)
}

func New() http.Handler {
	router := mux.NewRouter()
	router.Handle("/package/{package}/{version}", http.HandlerFunc(packageHandler))
	return router
}

type npmPackageMetaResponse struct {
	Versions map[string]npmPackageResponse `json:"versions"`
}

// review: method to extract and convert all versions to a comma-separated string
func (r *npmPackageMetaResponse) GetVersionsAsString() string {
	// review: get all the keys (versions) from the Versions map
	versions := make([]string, 0, len(r.Versions))
	for version := range r.Versions {
		versions = append(versions, version)
	}

	// review: sort versions (optional, remove this line if order doesn't matter)
	sort.Strings(versions)

	// review: limit the number of versions to a maximum of 10
	if len(versions) > 10 {
		versions = versions[:10]
	}

	// review: join the versions into a single comma-separated string
	return strings.Join(versions, ", ")
}

type npmPackageResponse struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
}

type NpmPackageVersion struct {
	Name         string                        `json:"name"`
	Version      string                        `json:"version"`
	Dependencies map[string]*NpmPackageVersion `json:"dependencies"`
}

func packageHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	pkgName := vars["package"]
	pkgVersion := vars["version"]

	// review: maps a package with its dependecies
	rootPkg := &NpmPackageVersion{Name: pkgName, Dependencies: map[string]*NpmPackageVersion{}}
	if err := resolveDependencies(rootPkg, pkgVersion); err != nil {
		println(err.Error())
		if httpErr, ok := err.(*HTTPError); ok {
			sendJSONError(w, httpErr)
		} else {
			sendJSONError(w, NewHTTPError(500, "internal server error"))
		}
		return
	}

	stringified, err := json.MarshalIndent(rootPkg, "", "  ")
	if err != nil {
		sendJSONError(w, NewHTTPError(500, "failed to marshal JSON"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)

	// Ignoring ResponseWriter errors
	_, _ = w.Write(stringified)
}

// review: recursively resolves a tree of packages and their associated depdencies
func resolveDependencies(pkg *NpmPackageVersion, versionConstraint string) error {
	// review: retuns a list of all published versions including dependencies
	pkgMeta, err := fetchPackageMeta(pkg.Name)
	if err != nil {
		return err
	}
	// review: collects all compatible versions, sorts them and then returns the highest compatible version
	concreteVersion, err := highestCompatibleVersion(versionConstraint, pkgMeta)
	if err != nil {
		return err
	}
	pkg.Version = concreteVersion

	npmPkg, err := fetchPackage(pkg.Name, pkg.Version)
	if err != nil {
		return err
	}
	// review: create a WaitGroup to wait for all dependencies to resolve
	var wg sync.WaitGroup
	var mu sync.Mutex // To protect shared resources (like pkg.Dependencies) and track errors
	var firstError error
	// review: for each depedent package recusively find its package, name and depdencies, i.e. creating
	// the depdency tree
	for dependencyName, dependencyVersionConstraint := range npmPkg.Dependencies {
		wg.Add(1) // reveiw: increment the wait counter
		// review: have multiple package dependencies resolved simultaneously,
		// potentially reducing the overall resolution time.
		go func(depName, depVersion string) {
			defer wg.Done() // review: decrement the wait counter when done
			dep := &NpmPackageVersion{Name: depName, Dependencies: map[string]*NpmPackageVersion{}}
			if err := resolveDependencies(dep, depVersion); err != nil {
				mu.Lock()
				if firstError == nil { // Capture only the first error
					firstError = err
				}
				mu.Unlock()
				return
			}
			// review: add the resolved dependency to the parent's dependency list
			mu.Lock()
			pkg.Dependencies[depName] = dep
			mu.Unlock()
		}(dependencyName, dependencyVersionConstraint)
	}
	// review: wait for all goroutines to complete
	wg.Wait()
	// review: return the first error encountered, if any
	if firstError != nil {
		return firstError
	}
	return nil
}

func highestCompatibleVersion(constraintStr string, versions *npmPackageMetaResponse) (string, error) {
	constraint, err := semver.NewConstraint(constraintStr)
	if err != nil {
		return "", NewHTTPError(http.StatusNotFound, fmt.Sprintf("unable to determine version constraint %s: %v", constraintStr, err))
	}
	filtered := filterCompatibleVersions(constraint, versions)
	sort.Sort(filtered)
	if len(filtered) == 0 {
		versionStr := versions.GetVersionsAsString()
		return "", NewHTTPError(http.StatusNotFound, fmt.Sprintf("no compatabile versions %s for constraint %s: %v", versionStr, constraintStr, err))
	}
	return filtered[len(filtered)-1].String(), nil
}

func filterCompatibleVersions(constraint *semver.Constraints, pkgMeta *npmPackageMetaResponse) semver.Collection {
	var compatible semver.Collection
	for version := range pkgMeta.Versions {
		semVer, err := semver.NewVersion(version)
		if err != nil {
			continue
		}
		if constraint.Check(semVer) {
			compatible = append(compatible, semVer)
		}
	}
	return compatible
}

func fetchPackage(name, version string) (*npmPackageResponse, error) {
	resp, err := http.Get(fmt.Sprintf("https://registry.npmjs.org/%s/%s", name, version))
	if err != nil {

		// review: process npm registry response
		if urlErr, ok := err.(*url.Error); ok {
			// review: determine if the error is a network error or an HTTP status error
			if urlErr.Timeout() {
				return nil, NewHTTPError(408, fmt.Sprintf("request timed out for package %s@%s: %v", name, version, urlErr))
			}
			// review: this case could be a DNS error, connection refused, etc.
			return nil, NewHTTPError(502, fmt.Sprintf("bad gateway while fetching package %s@%s: %v", name, version, urlErr))
		}
		// review: fallback for any other type of error
		return nil, NewHTTPError(500, fmt.Sprintf("failed to fetch package %s@%s: %v", name, version, err))
	}

	// reveiw: handle HTTP response
	// review: defer closing the body
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, NewHTTPError(resp.StatusCode, fmt.Sprintf("Unable to find package %s@%s", name, version))
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewHTTPError(resp.StatusCode, fmt.Sprintf("received unexpected status %d for package %s@%s", resp.StatusCode, name, version))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewHTTPError(500, fmt.Sprintf("unable to read response body for package %s@%s: %v", name, version, err))
	}

	var parsed npmPackageResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, NewHTTPError(500, fmt.Sprintf("unable to pars package metadata %s@%s: %v", name, version, err))
	}
	return &parsed, nil
}

func fetchPackageMeta(p string) (*npmPackageMetaResponse, error) {
	resp, err := http.Get(fmt.Sprintf("https://registry.npmjs.org/%s", p))
	if err != nil {

		// review: process npm registry response
		if urlErr, ok := err.(*url.Error); ok {
			// review: determine if the error is a network error or an HTTP status error
			if urlErr.Timeout() {
				return nil, NewHTTPError(408, fmt.Sprintf("request timed out for package %s: %v", p, urlErr))
			}
			// review: this case could be a DNS error, connection refused, etc.
			return nil, NewHTTPError(502, fmt.Sprintf("bad gateway while fetching package %s: %v", p, urlErr))
		}
		// review: fallback for any other type of error
		return nil, NewHTTPError(500, fmt.Sprintf("failed to fetch package %s: %v", p, err))
	}

	// reveiw: handle HTTP response
	// review: defer closing the body
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, NewHTTPError(resp.StatusCode, fmt.Sprintf("unable to find package %s", p))
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewHTTPError(resp.StatusCode, fmt.Sprintf("received unexpected status %d for package %s", resp.StatusCode, p))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewHTTPError(500, fmt.Sprintf("unable to read response body for package %s: %v", p, err))
	}

	var parsed npmPackageMetaResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, NewHTTPError(500, fmt.Sprintf("unable to pars package metadata %s: %v", p, err))
	}

	return &parsed, nil
}
