# Adjacent unmatched bracket before a real link

Two open square brackets with nothing between them still resolve against the nearer one, and a stray closing bracket inside the destination does not end the link early: [[]((a)]"b) stays byte-for-byte, even though it looks like ordinary "prose" right next to it.
