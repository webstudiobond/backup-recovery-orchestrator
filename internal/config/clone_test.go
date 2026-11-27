package config

import (
	"reflect"
	"testing"
)

func TestBundleCloneNilAndZero(t *testing.T) {
	t.Parallel()

	var nilBundle *Bundle
	if got := nilBundle.Clone(); !reflect.DeepEqual(got, Bundle{}) {
		t.Fatalf("expected zero Bundle on nil Clone, got %+v", got)
	}

	empty := Bundle{
		ConfigDir: "/etc/bro",
		Global: GlobalConfig{
			BackupRoot: "/backups",
		},
	}
	cloned := empty.Clone()
	if !reflect.DeepEqual(cloned, empty) {
		t.Fatalf("expected equal empty clone, got %+v", cloned)
	}
	if cloned.Stacks != nil || cloned.Global.Destinations != nil || cloned.Global.Retention.RemoteOverrides != nil {
		t.Fatalf("expected nil slices/maps preserved in clone: %+v", cloned)
	}

	minimalStackBundle := Bundle{
		Stacks: []StackConfig{{Name: "minimal-stack"}},
	}
	minimalCloned := minimalStackBundle.Clone()
	if !reflect.DeepEqual(minimalCloned, minimalStackBundle) {
		t.Fatalf("expected equal minimal stack clone, got %+v", minimalCloned)
	}
	if minimalCloned.Stacks[0].Databases != nil ||
		minimalCloned.Stacks[0].RestoreSteps != nil ||
		minimalCloned.Stacks[0].Hooks.PreDump != nil {
		t.Fatalf("expected nil nested slices preserved on stack clone: %+v", minimalCloned.Stacks[0])
	}
}

func TestBundleCloneDeepIsolation(t *testing.T) {
	t.Parallel()

	orig := buildPopulatedCloneTestBundle()
	snapshot := buildPopulatedCloneTestBundle()
	cloned := orig.Clone()

	if !reflect.DeepEqual(cloned, orig) {
		t.Fatalf("expected clone to match original:\ngot:  %+v\nwant: %+v", cloned, orig)
	}
	if !cloned.Stacks[0].HasReverseProxyBlock() {
		t.Fatal("expected unexported hasReverseProxyBlock to be preserved by Clone")
	}

	mutateClonedBundle(&cloned)

	if !reflect.DeepEqual(orig, snapshot) {
		t.Fatalf("mutating cloned bundle affected original:\norig:     %+v\nsnapshot: %+v", orig, snapshot)
	}
}

func buildPopulatedCloneTestBundle() Bundle {
	destCount := 5
	return Bundle{
		ConfigDir: "/etc/bro",
		Global: GlobalConfig{
			BackupRoot:        "/backups",
			ReverseProxyStack: "edge-proxy",
			Retention: RetentionConfig{
				LocalCount:      2,
				RemoteCount:     4,
				RemoteOverrides: map[string]int{"clone-dest-a": 9},
			},
			Destinations: []DestinationConfig{
				{
					Name:        "clone-dest-b",
					Type:        DestTypeDir,
					BackupDir:   "/mnt/clone-dest-b",
					RemoteCount: &destCount,
				},
			},
		},
		Stacks: []StackConfig{
			{
				Name:                 "clone-stack-one",
				StackDir:             "/home/clone-stack-one",
				ComposeFile:          "/home/clone-stack-one/docker-compose.yaml",
				Vars:                 map[string]string{"env": "prod"},
				hasReverseProxyBlock: true,
				ReverseProxyProvider: &ReverseProxyProviderConfig{
					Type:        "angie",
					ServiceName: "edge",
					CertsDir:    "certs",
					DomainsDir:  "domains",
					StreamDir:   "stream",
				},
				ReverseProxy: StackReverseProxyConfig{
					ACMENames:   []string{"clone.example.com"},
					DomainConfs: []string{"clone.example.com.conf"},
					StreamConfs: []string{"tcp.clone.example.com.inc"},
				},
				Hooks: HooksConfig{
					PreDump:      []HookStep{{Action: "compose_exec", Service: "web-pre", Command: []string{"echo", "pre"}}},
					PreSnapshot:  []HookStep{{Action: "compose_kill", Service: "web-snap", Signal: "SIGUSR1"}},
					PostSnapshot: []HookStep{{Action: "compose_run", Service: "web-post", Command: []string{"echo", "post"}}},
				},
				Destinations: []string{"clone-dest-c"},
				DAC:          []DACRule{{Path: "data", Owner: "clone-stack-one:clone-stack-one", DirMode: "0750"}},
				HostFiles:    []HostFileConfig{{Path: "/etc/clone-stack-one/conf.ini", Owner: "clone-stack-one:clone-stack-one", FileMode: "0640"}},
				Databases: []DatabaseConfig{
					{
						ID:      "clone-db",
						Driver:  "postgres",
						Service: "pg-srv",
						Secrets: &DBSecretsConfig{
							Source:   "host",
							UserFile: "/var/clone/db_user",
						},
					},
				},
				RestoreSteps: []RestoreStep{
					{
						Action:   RestoreActionComposeExec,
						Service:  "web-step",
						Services: []string{"web-step"},
						Args:     []string{"--check"},
						Command:  []string{"bin/migrate"},
					},
				},
				Retention: RetentionConfig{
					LocalCount:      3,
					RemoteCount:     6,
					RemoteOverrides: map[string]int{"clone-dest-d": 12},
				},
				Files: FilesConfig{
					Excludes: []string{"*.clone-bak"},
				},
			},
		},
	}
}

func mutateClonedBundle(cloned *Bundle) {
	cloned.ConfigDir = "/mutated/dir"
	cloned.Global.BackupRoot = "/mutated/backups"
	cloned.Global.Retention.RemoteOverrides["clone-dest-a"] = 999
	*cloned.Global.Destinations[0].RemoteCount = 777
	cloned.Global.Destinations[0].Name = "mutated-dest"

	st := &cloned.Stacks[0]
	st.Name = "mutated-stack"
	st.Vars["env"] = "mutated"
	st.ReverseProxyProvider.ServiceName = "mutated-svc"
	st.ReverseProxy.ACMENames[0] = "mutated.example.org"
	st.ReverseProxy.DomainConfs[0] = "mutated.example.org.conf"
	st.ReverseProxy.StreamConfs[0] = "mutated-stream.example.org.inc"
	st.Hooks.PreDump[0].Command[0] = "mutated-pre"
	st.Hooks.PreSnapshot[0].Signal = "SIGKILL"
	st.Hooks.PostSnapshot[0].Command[0] = "mutated-post"
	st.Destinations[0] = "mutated-dest"
	st.DAC[0].Path = "mutated-data"
	st.HostFiles[0].Path = "/mutated/host"
	st.Databases[0].Secrets.UserFile = "/mutated/db_user"
	st.RestoreSteps[0].Services[0] = "mutated-svc"
	st.RestoreSteps[0].Args[0] = "--mutated"
	st.RestoreSteps[0].Command[0] = "mutated-cmd"
	st.Retention.RemoteOverrides["clone-dest-d"] = 888
	st.Files.Excludes[0] = "*.mutated"
}
