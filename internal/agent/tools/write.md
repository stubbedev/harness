Create or overwrite a file with given content; auto-creates parent dirs. Cannot append. Read the file first to avoid conflicts. For surgical changes use edit.

Overwriting requires reading the entire current file (possibly across multiple `view` calls), or content previously created by the tools. A partial read is insufficient. A conflict refuses the write and returns a bounded current excerpt; read any remaining ranges before retrying. Shell output does not certify reads.
