// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package skill

import "errors"

var (
	// ErrMissingYAMLFrontmatter is returned when a SKILL.md file does not contain YAML frontmatter.
	ErrMissingYAMLFrontmatter = errors.New("missing YAML frontmatter")
	// ErrInvalidSkillFormat is returned when a SKILL.md file has an invalid format.
	ErrInvalidSkillFormat = errors.New("invalid SKILL.md format")
	// ErrMissingDescription is returned when a SKILL.md carries no description.
	// The description is the whole of how a skill reaches the model — the
	// prompt catalog lists name, description and path, and the model decides
	// from the description alone whether a skill applies — so a skill without
	// one is never loaded, listed, or offered anywhere, and creating or
	// installing it would only leave the user a skill that does nothing.
	ErrMissingDescription = errors.New("a skill needs a description in its SKILL.md frontmatter, or it is never loaded")
	// ErrGitMissing is returned when a package has to be fetched with git and
	// git is not on PATH.
	ErrGitMissing = errors.New("git is not installed, so skill packages cannot be fetched")
	// ErrNoSkillsInPackage is returned when a fetched package holds no
	// SKILL.md anywhere.
	ErrNoSkillsInPackage = errors.New("the package contains no skill")
	// ErrUnsupportedSource is returned for a reference that is neither a git
	// URL nor an owner/repo pair.
	ErrUnsupportedSource = errors.New("the source must be a git URL or an owner/repo pair")
)
