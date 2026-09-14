package term

// SessionsSupported is false on Windows: the pty package has no
// implementation there, so Start always fails. Offering a terminal that
// can never open is worse than not offering one, so callers check this
// and leave the shell tool out of the tool set entirely.
const SessionsSupported = false
