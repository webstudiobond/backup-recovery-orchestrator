package reverseproxy

// TypeAngie identifies the built-in Angie reverse-proxy provider.
const TypeAngie = "angie"

type angieProvider struct{}

func (angieProvider) Type() string {
	return TypeAngie
}

func (angieProvider) SyntaxCheckArgs() []string {
	return []string{TypeAngie, "-t"}
}

func (angieProvider) DefaultConfDACPolicy(groupName string) DACPolicy {
	return DACPolicy{
		Owner:    "root:" + groupName,
		FileMode: "0440",
	}
}

func (angieProvider) DefaultCertDACPolicy(ownerName string) DACPolicy {
	return DACPolicy{
		Owner:     ownerName + ":" + ownerName,
		DirMode:   "0700",
		FileMode:  "0600",
		Recursive: true,
	}
}
