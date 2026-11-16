package gitops

// ShortSHALen is how many characters of a commit hash Dash shows.
const ShortSHALen = 8

// ShortSHA is the first ShortSHALen characters of sha, or all of it when
// it's shorter. It's for display only: never compare or look up by it.
func ShortSHA(sha string) string {
	if len(sha) > ShortSHALen {
		return sha[:ShortSHALen]
	}
	return sha
}
