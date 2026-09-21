# Security

Report suspected vulnerabilities through
[GitHub's private reporting form](https://github.com/olivere/systemone/security/advisories/new).
Do not open a public issue with exploit details, credentials, or customer data.

Include the affected version or commit, a minimal reproduction with synthetic
data, and the expected impact. Security fixes target the latest release and
`main`; older releases have no separate maintenance commitment.

HTTP debugging is opt-in. It redacts credential headers but not request or
response bodies. Model probabilities and confidence are not authorization
checks; applications must enforce their own access controls.
