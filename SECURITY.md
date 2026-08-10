# Security

mdreflow is designed to be safe to run unattended on untrusted trees: non-regular files are refused, writes are atomic, discovered config files are size-capped and screened, and reflow falls back to a no-op rather than ever changing what a document renders to.
If you find a way through any of that, I want to know.

Report vulnerabilities privately via [GitHub's private vulnerability reporting](https://github.com/jbeda/mdreflow/security/advisories/new) (Security tab → "Report a vulnerability").

This is a solo-maintained project.
I will acknowledge reports, but I don't promise response or fix timelines.
For anything that isn't sensitive, a public issue is fine and usually faster.
