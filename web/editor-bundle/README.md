# Editor bundle

Builds `web/static/vendor/codemirror.js`, the CodeMirror 6 module the prompt
editor (`web/static/editor.js`) imports. The output is committed, so `go build`
needs no Node. Rebuild after changing `entry.js` or the dependencies:

    npm ci
    npm run build
