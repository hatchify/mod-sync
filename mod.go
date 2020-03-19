package sync

import (
	"os"
	"os/exec"
	"path"
	"strings"

	sort "github.com/hatchify/mod-sort"
)

// CleanModCache calls go clean --modcache from calling directory. No context necessary
func CleanModCache() error {
	cmd := exec.Command("go", "clean", "--modcache")
	return cmd.Run()
}

// ModInit calls go mod init on a given lib
func (lib *Library) ModInit() error {
	return lib.File.RunCmd("go", "mod", "init")
}

// ModTidy calls go mod tidy on a given lib
func (lib *Library) ModTidy() error {
	return lib.File.RunCmd("go", "mod", "tidy")
}

// ModClearFiles calls rm go.mod and rm go.sum, returning the success of both commands
func (lib *Library) ModClearFiles() (hasModFile, hasSumFile bool) {
	if lib.File.RunCmd("rm", "go.mod") == nil {
		hasModFile = true
	}

	if lib.File.RunCmd("rm", "go.sum") == nil {
		hasSumFile = true
	}

	return
}

// ModAddDeps adds a dep@version to go.mod to force-update or force-downgrade any deps in the filtered chain
func (lib *Library) ModAddDeps(listHead *sort.FileNode) {
	for itr := listHead; itr != nil && itr.File.Path != lib.File.Path; itr = itr.Next {
		// Check if lib/go.mod includes the file (not go.sum)
		if lib.File.ImportsDirectly(itr.File) {
			// Create new node to add to independent list on lib with same file ref
			var node sort.FileNode
			node.File = itr.File
			lib.AddDep(&node)
		}
	}
}

// ModSetDeps adds a dep@version to go.mod to force-update or force-downgrade any deps in the filtered chain
func (lib *Library) ModSetDeps() {
	// Iterate through dep chain
	for itr := lib.updatedDeps; itr != nil; itr = itr.Next {
		if len(itr.File.Version) == 0 {
			lib.File.Output("Error: no version to set for " + itr.File.Path)

		} else {
			url := itr.File.GetGoURL()

			// Get dep @ version (-d avoids building)
			if lib.File.RunCmd("go", "get", "-d", url+"@"+itr.File.Version) == nil {
				if itr.File.Updated || itr.File.Tagged || itr.File.Deployed {
					lib.File.Output("Updated " + url + " @ " + itr.File.Version)
				}
			} else {
				lib.File.Output("Error: Failed to get " + url + " @ " + itr.File.Version)
			}
		}
	}

	lib.AppendToModfile("// *** Separate Local Deps *** \\\\")
}

// SetLocalDep adds replace clause for provided file
func (lib *Library) SetLocalDep(file sort.FileNode) (updated bool) {
	return lib.AppendToModfile(file.File.GetGoURL())
}

// SetLocalDeps adds replace clause for all updated deps
func (lib *Library) SetLocalDeps() (updated bool) {
	localSuffix := ""
	for fileItr := lib.updatedDeps; fileItr != nil; fileItr = fileItr.Next {
		localSuffix += "replace " + fileItr.File.GetGoURL() + " => ../../../" + fileItr.File.GetGoURL() + "\n"
	}

	return lib.AppendToModfile(localSuffix)
}

// AppendToModfile appends provided string to end of mod file
func (lib *Library) AppendToModfile(text string) bool {
	// Open absolute path to mod file in append mode
	f, err := os.OpenFile(path.Join(lib.File.AbsPath(), "go.mod"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		lib.File.Output("Unable to open mod file: " + path.Join(lib.File.AbsPath(), "go.mod"))
		return false
	}
	defer f.Close()

	// Append message
	if _, err := f.WriteString(text + "\n"); err != nil {
		lib.File.Output("Unable to write to mod file")
		return false
	}

	// Write successful
	return true
}

// ModDeploy will commit and push local changes to the current branch before switching to master
func (lib *Library) ModDeploy(tag string) (deployed bool) {
	// Handle saving local changes
	lib.File.StashPop()
	lib.File.Add(".")

	// Ignore changes to go mod files (prevents committing local replacements)
	lib.File.Reset("go.*")

	message := ""
	if len(tag) == 0 {
		version := lib.File.Version
		if len(version) == 0 && !strings.HasSuffix(strings.Trim(lib.File.Path, "/"), "-plugin") {
			version = lib.GetCurrentTag()
		}

		if len(version) == 0 {
			message = "gomu: Deploy local changes"
			// Set old version of libs in case they weren't updated previously
			lib.File.Version = version

		} else {
			message = "gomu: Deploy local changes before incrementing version from " + version
		}
	} else {
		message = "gomu: Deploy local changes before updating version to " + tag
	}

	if lib.File.Commit(message) == nil {
		// Successful commit, push changes
		deployed = true
		lib.File.Output("Deploying local changes...")
		if lib.File.Push() == nil {
			lib.File.Output("Deploy Complete!")
		} else {
			lib.File.Output("Deploy Failed :(")
		}
	} else {
		lib.File.Output("No changes to deploy!")
	}

	// Re-stash local changes to go.mod
	lib.File.Stash()

	return
}

// ModUpdate will refresh the current dir to master, reset mod files and push changes if there are any
func (lib *Library) ModUpdate(branch, commitMessage string) (err error) {
	lib.File.Output("Syncing " + branch + " with origin master...")

	if branch != "master" {
		if err = lib.File.CheckoutBranch("master"); err != nil {
			lib.File.Output("Checkout master failed :(")
		}
	}

	if err = lib.File.Fetch(); err != nil {
		lib.File.Output("Fetch failed :(")
	}

	if err = lib.File.Pull(); err != nil {
		lib.File.Output("Pull failed :(")
		return
	}

	if branch != "master" {
		if err = lib.File.CheckoutBranch(branch); err != nil {
			lib.File.Output("Checkout " + branch + " failed :(")
		}
		if err = lib.File.Merge("master"); err != nil {
			lib.File.Output("Merge master into " + branch + " failed :(")
		}
	}

	lib.File.Output("Checking deps...")

	// Remove go.mod, ignore lib if not found (not a mod tracked lib)
	if lib.File.RunCmd("rm", "go.mod") != nil {
		lib.File.Output("No mod file found. Skipping.")
		return
	}

	// Remove go sum to prevent mess from adding up
	if lib.File.RunCmd("rm", "go.sum") == nil {
		// No dependencies found. If this is unexpected for a given lib, something is out of sync
		lib.File.Output("No sum file found. No dependencies sorted.")
	}

	if err = lib.ModInit(); err != nil {
		lib.File.Output("Mod init failed :(")
		return
	}

	lib.ModSetDeps()

	if err = lib.ModTidy(); err != nil {
		lib.File.Output("Mod tidy failed :(")
		return
	}

	if err = lib.File.Add("go.*"); err != nil {
		lib.File.Output("Git add failed :(")
		return
	}

	message := "gomu: " + commitMessage + "\n"
	for itr := lib.updatedDeps; itr != nil; itr = itr.Next {
		url := itr.File.GetGoURL()

		if itr.File.Updated {
			message += "\nUpdated " + url + "@" + itr.File.Version
		} else {
			message += "\nSet " + url + "@" + itr.File.Version
		}
	}

	if err = lib.File.Commit(message); err == nil {
		lib.File.Output("Updating mod files...")
	} else {
		lib.File.Output("Deps up to date!")
	}

	if pushErr := lib.File.Push(); pushErr != nil {
		lib.File.Output("Push failed :( check local changes")
		return pushErr
	}

	lib.File.Output("Mod Sync Complete!")
	return
}
