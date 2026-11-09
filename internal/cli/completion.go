package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// commandUsages maps each command to its help text, which also lists the
// options that completion offers.
var commandUsages = map[string]string{
	"list":       listUsage,
	"export":     exportUsage,
	"import":     importUsage,
	"inspect":    inspectUsage,
	"save":       saveUsage,
	"open":       openUsage,
	"status":     statusUsage,
	"doctor":     doctorUsage,
	"completion": completionUsage,
}

// optionLine matches an option in a help text, with its value placeholder.
var optionLine = regexp.MustCompile(`(?m)^ {2}(?:(-[a-z]), )?(--[a-z][a-z-]*)(?: ([A-Z]+))?`)

// option is a command-line option as its help text describes it.
type option struct {
	names    []string
	argument string
}

// options returns the options a help text describes.
func options(usage string) []option {
	var found []option
	for _, match := range optionLine.FindAllStringSubmatch(usage, -1) {
		names := []string{match[2]}
		if match[1] != "" {
			names = append(names, match[1])
		}
		found = append(found, option{names: names, argument: match[3]})
	}
	return found
}

// filesCandidate tells the completion script to complete file paths.
const filesCandidate = ":files"

// complete prints completion candidates, one per line as a value and an
// optional tab-separated description, for words: the arguments after the
// program name, the last being the word under the cursor.
func (a *App) complete(ctx context.Context, words []string) error {
	candidates := a.candidates(ctx, words)
	for _, candidate := range candidates {
		if _, err := fmt.Fprintln(a.Stdout, candidate); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) candidates(ctx context.Context, words []string) []string {
	if len(words) == 0 {
		return nil
	}
	current := words[len(words)-1]
	if len(words) == 1 {
		names := make([]string, 0, len(commandUsages))
		for name := range commandUsages {
			names = append(names, name)
		}
		slices.Sort(names)
		return names
	}
	command := words[0]
	usage, ok := commandUsages[command]
	if !ok {
		return nil
	}
	commandOptions := options(usage)
	if previous := words[len(words)-2]; strings.HasPrefix(previous, "-") {
		for _, candidate := range commandOptions {
			if slices.Contains(candidate.names, previous) && candidate.argument != "" {
				if candidate.argument == "PATH" {
					return []string{filesCandidate}
				}
				return nil
			}
		}
	}
	if strings.HasPrefix(current, "-") {
		var names []string
		for _, candidate := range commandOptions {
			names = append(names, candidate.names[0])
		}
		return names
	}
	switch command {
	case "export", "save":
		return a.sessionCandidates(ctx, slices.Contains(words, "--all"))
	case "open":
		return a.savedCandidates(ctx)
	case "import", "inspect":
		return []string{filesCandidate}
	case "completion":
		return []string{"bash", "zsh"}
	}
	return nil
}

// sessionCandidates returns local session IDs, described by their titles.
func (a *App) sessionCandidates(ctx context.Context, includeAll bool) []string {
	quiet := *a
	quiet.Stderr = io.Discard
	opened, err := quiet.openStores(ctx)
	if err != nil {
		return nil
	}
	defer opened.close()
	sessions, err := quiet.sessions(opened, includeAll)
	if err != nil {
		return nil
	}
	candidates := make([]string, 0, len(sessions))
	for _, session := range sessions {
		candidates = append(candidates, session.ID+"\t"+shorten(session.Title))
	}
	return candidates
}

// savedCandidates returns the names of the sessions saved in the repository.
func (a *App) savedCandidates(ctx context.Context) []string {
	repo, err := currentRepository(ctx)
	if err != nil {
		return nil
	}
	saved, err := repo.Sessions()
	if err != nil {
		return nil
	}
	candidates := make([]string, 0, len(saved))
	for _, session := range saved {
		candidates = append(candidates, session.Name)
	}
	return candidates
}

func (a *App) completion(args []string) error {
	set := newFlagSet("completion")
	positional, err := a.parse(set, args, completionUsage)
	if err != nil || len(positional) != 1 {
		return usageOrHelp(err, "completion requires a shell: bash or zsh")
	}
	script, ok := completionScripts[positional[0]]
	if !ok {
		return &UsageError{Message: fmt.Sprintf("unsupported shell %q; use bash or zsh", positional[0])}
	}
	_, err = io.WriteString(a.Stdout, script)
	return err
}

// completionScripts ask agent-teleport __complete for candidates, so that
// completion follows the installed version.
var completionScripts = map[string]string{
	"bash": `# bash completion for agent-teleport
_agent_teleport() {
    local current=${COMP_WORDS[COMP_CWORD]} candidates
    candidates=$(agent-teleport __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null) || return
    if [[ $candidates == ":files" ]]; then
        COMPREPLY=($(compgen -f -- "$current"))
        return
    fi
    local IFS=$'\n'
    COMPREPLY=($(compgen -W "$(cut -f1 <<<"$candidates")" -- "$current"))
}
complete -o filenames -F _agent_teleport agent-teleport
`,
	"zsh": `#compdef agent-teleport
# zsh completion for agent-teleport
_agent_teleport() {
    local -a candidates described
    local line
    candidates=("${(@f)$(agent-teleport __complete "${(@)words[2,CURRENT]}" 2>/dev/null)}")
    if [[ ${candidates[1]} == ":files" ]]; then
        _files
        return
    fi
    for line in "${candidates[@]}"; do
        [[ -n $line ]] || continue
        described+=("${${line%%$'\t'*}//:/\\:}:${line#*$'\t'}")
    done
    _describe -t values agent-teleport described
}
if [[ ${funcstack[1]} == _agent_teleport ]]; then
    _agent_teleport "$@"
else
    compdef _agent_teleport agent-teleport
fi
`,
}
