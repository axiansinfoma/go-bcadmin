package bcadmin

import (
	"runtime/debug"
	"sync"
)

const (
	// DefaultAPIVersion is the Admin Center API version used when
	// ClientOptions.APIVersion is empty.
	DefaultAPIVersion = "v2.29"

	// modulePath is the import path of this module, used to find its version
	// in the build information of the program.
	modulePath = "github.com/axiansinfoma/go-bcadmin"

	// moduleName is the name reported in the User-Agent header.
	moduleName = "go-bcadmin"
)

// moduleVersion returns the version of this module as recorded in the build
// information of the running program, or "devel" when it is not available
// (for example in this module's own tests).
var moduleVersion = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if info.Main.Path == modulePath && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			if dep.Version != "" {
				return dep.Version
			}
		}
	}
	return "devel"
})
