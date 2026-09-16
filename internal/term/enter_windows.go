package term

// Enter is the byte sequence that submits a line to the session's
// shell. ConPTY turns the input stream into key events, and only a
// carriage return is the Enter key there: a bare line feed reaches
// cmd.exe and PowerShell as ctrl-j, which neither reads as "run this".
const Enter = "\r"
