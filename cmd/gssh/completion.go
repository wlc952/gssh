package main

import (
	"fmt"
	"strings"
)

// handleCompletion prints a shell completion script. Keeping the scripts in
// the binary makes installation straightforward in constrained CI/container
// environments (for example: eval "$(gssh completion bash)").
func handleCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: gssh completion <bash|zsh|fish>")
	}
	switch strings.ToLower(args[0]) {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		return fmt.Errorf("unsupported shell %q (choose bash, zsh, or fish)", args[0])
	}
	return nil
}

const bashCompletion = `# bash completion for gssh (install: gssh completion bash > /etc/bash_completion.d/gssh)
_gssh_completions() {
    local cur prev cmd
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    cmd="${COMP_WORDS[1]}"
    local commands="connect local exec run list ls status use kill cancel tasks batch forward forwards forward-close scp sync sftp start stop ping server help completion"
    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
        return 0
    fi
    case "${cmd}" in
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") ); return 0 ;;
        connect)
            COMPREPLY=( $(compgen -W "-p -i -n --pswd --password-stdin --connect-timeout" -- "${cur}") ); return 0 ;;
        use)
            COMPREPLY=( $(compgen -W "-i -n --pswd --password-stdin" -- "${cur}") ); return 0 ;;
        exec|run)
            COMPREPLY=( $(compgen -W "-n -t -e --stream --task-id --raw --sudo --pswd --sudo-user --sudo-login --cwd --stdin --stdin-file" -- "${cur}") ); return 0 ;;
        local|kill)
            COMPREPLY=( $(compgen -W "-n" -- "${cur}") ); return 0 ;;
        list|ls|status|forwards)
            COMPREPLY=( $(compgen -W "--json" -- "${cur}") ); return 0 ;;
        batch)
            COMPREPLY=( $(compgen -W "-f -p -i --parallel -t -e --raw --pswd --password-stdin --connect-timeout" -- "${cur}") ); return 0 ;;
        forward)
            COMPREPLY=( $(compgen -W "-n -L -R --bind --public" -- "${cur}") ); return 0 ;;
        scp|sync)
            COMPREPLY=( $(compgen -W "-n" -- "${cur}") ); return 0 ;;
        sftp)
            COMPREPLY=( $(compgen -W "-n" -- "${cur}") ); return 0 ;;
    esac
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "-S -v --version -h --help" -- "${cur}") )
    fi
}
complete -F _gssh_completions gssh
`

const zshCompletion = `#compdef gssh
# zsh completion for gssh (install: gssh completion zsh > ~/.zsh/completions/_gssh)
_gssh() {
  local -a commands
  commands=(
    'connect:connect to an SSH destination'
    'local:create a local session'
    'exec:execute a command in a session'
    'run:execute a one-off local command'
    'list:list sessions' 'ls:list sessions' 'status:list sessions'
    'use:switch the default session' 'kill:kill a session'
    'cancel:cancel a running task' 'tasks:list running tasks'
    'batch:execute on multiple hosts' 'forward:create a port forward'
    'forwards:list forwards' 'forward-close:close a forward'
    'scp:copy files' 'sync:copy files and skip unchanged files'
    'sftp:run an SFTP operation' 'start:start daemon' 'stop:stop daemon'
    'ping:ping daemon' 'server:run daemon' 'completion:generate shell completion'
    'help:show help')
  if (( CURRENT == 2 )); then
    _describe 'command' commands
    return
  fi
  case $words[2] in
    completion) _values 'shell' bash zsh fish ;;
    connect) _arguments '-p[SSH port]:port:' '-i[identity file]:key file:_files' '-n[session name]:name:' '--pswd[SSH password]:password:' '--password-stdin[read password from stdin]' '--connect-timeout[connection timeout]:seconds:' ;;
    use) _arguments '-i[identity file]:key file:_files' '-n[session name]:name:' '--pswd[SSH password]:password:' '--password-stdin[read password from stdin]' ;;
    exec|run) _arguments '-n[session name]:name:' '-t[command timeout]:seconds:' '-e[environment]:KEY=VALUE:' '--stream[stream output]' '--task-id[task identifier]:id:' '--raw[raw output]' '--sudo[run with sudo]' '--pswd[sudo password]:password:' '--sudo-user[run as user]:user:' '--sudo-login[login shell]' '--cwd[working directory]:directory:_directories' '--stdin[pass stdin]' '--stdin-file[stdin file]:file:_files' '*:command:' ;;
    local|kill) _arguments '-n[session name]:name:' ;;
    list|ls|status|forwards) _arguments '--json[JSON output]' ;;
    batch) _arguments '-f[host file]:file:_files' '-p[SSH port]:port:' '-i[identity file]:key file:_files' '--parallel[max parallelism]:count:' '-t[timeout]:seconds:' '-e[environment]:KEY=VALUE:' '--raw[raw output]' '--pswd[SSH password]:password:' '--password-stdin[read password from stdin]' '--connect-timeout[connection timeout]:seconds:' ;;
    forward) _arguments '-n[session name]:name:' '-L[local forward]:spec:' '-R[remote forward]:spec:' '--bind[bind address]:address:' '--public[allow public bind]' ;;
    scp|sync|sftp) _arguments '-n[session name]:name:' '*:path:_files' ;;
  esac
}
compdef _gssh gssh
`

const fishCompletion = `# fish completion for gssh (install: gssh completion fish | source)
complete -c gssh -f -n '__fish_use_subcommand' -a 'connect local exec run list ls status use kill cancel tasks batch forward forwards forward-close scp sync sftp start stop ping server help completion'
complete -c gssh -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c gssh -n '__fish_seen_subcommand_from connect use' -s p -d 'SSH port'
complete -c gssh -n '__fish_seen_subcommand_from connect use' -s i -r -d 'identity file'
complete -c gssh -n '__fish_seen_subcommand_from connect use' -s n -r -d 'session name'
complete -c gssh -n '__fish_seen_subcommand_from connect use' -l pswd -r -d 'SSH password'
complete -c gssh -n '__fish_seen_subcommand_from connect use' -l password-stdin -d 'read password from stdin'
complete -c gssh -n '__fish_seen_subcommand_from exec run' -s n -l name -r -d 'session name'
complete -c gssh -n '__fish_seen_subcommand_from exec run' -s t -l timeout -r -d 'command timeout'
complete -c gssh -n '__fish_seen_subcommand_from exec run' -s e -l env -r -d 'environment KEY=VALUE'
complete -c gssh -n '__fish_seen_subcommand_from exec run' -l stream -l raw -l sudo -l stdin
complete -c gssh -n '__fish_seen_subcommand_from exec run' -l stdin-file -r
complete -c gssh -n '__fish_seen_subcommand_from exec run' -l sudo-user -r -l sudo-login -l cwd -r
complete -c gssh -n '__fish_seen_subcommand_from list ls status forwards' -l json
complete -c gssh -n '__fish_seen_subcommand_from local kill' -s n -l name -r
complete -c gssh -n '__fish_seen_subcommand_from batch' -s f -r -s p -r -s i -r -l parallel -r -s t -r -s e -r -l raw
complete -c gssh -n '__fish_seen_subcommand_from forward' -s n -l name -r -s L -s R -l bind -r -l public
complete -c gssh -n '__fish_seen_subcommand_from scp sync sftp' -s n -l name -r
complete -c gssh -s S -r -d 'socket path'
complete -c gssh -s v -l version -d 'show version'
`
