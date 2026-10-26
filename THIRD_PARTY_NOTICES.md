# Third-party notices

Toolbox original code is licensed under AGPL-3.0-only. Third-party components retain their own licenses and copyright notices. This inventory does not relicense them.

The `LICENSES/` directory preserves license and notice texts supplied with the exact Go module graph and installed npm dependency versions used for the 0.1.11 build. The Go module graph also contains build/development dependencies; inclusion here does not mean every listed module is linked into every platform binary. npm metadata marks development dependencies. The two MPL-2.0 packages (lightningcss and its platform binding) are build tools, not a change to the Toolbox project license. They are not modified or bundled as runnable components in these desktop ZIPs.

Some packages publish their license only in metadata: the Vue Devtools 6.6.4 notice was retrieved from its upstream release, Rolldown binding uses the same release's parent package license, and lodash-unified's MIT declaration and author metadata are retained with the standard permission text. Original supplied notices are preserved as-is.

Node.js, Python, WebView2 and separately installed MCP servers are not included in the desktop ZIPs; their own distributions and terms apply. The Go toolchain's runtime license is included because the runtime is linked into the executables.

Exact package inventory: [third-party-licenses.json](third-party-licenses.json).
