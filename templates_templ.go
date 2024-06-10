// Code generated by templ - DO NOT EDIT.

// templ: version: v0.2.707
package main

//lint:file-ignore SA4006 This context is only used if a nested component is present.

import "github.com/a-h/templ"
import "context"
import "io"
import "bytes"

func errpage(
	title, header, message string,
	printDebugLogs bool, wsEventsEndpoint string,
) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, templ_7745c5c3_W io.Writer) (templ_7745c5c3_Err error) {
		templ_7745c5c3_Buffer, templ_7745c5c3_IsBuffer := templ_7745c5c3_W.(*bytes.Buffer)
		if !templ_7745c5c3_IsBuffer {
			templ_7745c5c3_Buffer = templ.GetBuffer()
			defer templ.ReleaseBuffer(templ_7745c5c3_Buffer)
		}
		ctx = templ.InitializeContext(ctx)
		templ_7745c5c3_Var1 := templ.GetChildren(ctx)
		if templ_7745c5c3_Var1 == nil {
			templ_7745c5c3_Var1 = templ.NopComponent
		}
		ctx = templ.ClearChildren(ctx)
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"UTF-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\"><title>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		var templ_7745c5c3_Var2 string
		templ_7745c5c3_Var2, templ_7745c5c3_Err = templ.JoinStringErrs(title)
		if templ_7745c5c3_Err != nil {
			return templ.Error{Err: templ_7745c5c3_Err, FileName: `templates.templ`, Line: 12, Col: 17}
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var2))
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</title><style>\n\t\t\t\t:root {\n\t\t\t\t\t--background-color: rgb(0,0,0);\n\t\t\t\t\t--text-color: coral;\n\t\t\t\t\t--header-color: red;\n\t\t\t\t}\n\n\t\t\t\t@media (prefers-color-scheme: light) {\n\t\t\t\t\t:root {\n\t\t\t\t\t\t--background-color: rgb(255,255,255);\n\t\t\t\t\t\t--text-color: orangered;\n\t\t\t\t\t\t--header-color: red;\n\t\t\t\t\t}\n\t\t\t\t}\n\n\t\t\t\tbody {\n\t\t\t\t\tfont-family: monospace;\n\t\t\t\t\tbox-sizig: border-box;\n\t\t\t\t\tmargin: 0;\n\t\t\t\t\tpadding: 1rem;\n\t\t\t\t\tbackground-color: var(--background-color);\n\t\t\t\t\tcolor: var(--text-color);\n\t\t\t\t}\n\n\t\t\t\tbody > h3 {\n\t\t\t\t\tmargin-top: 0;\n\t\t\t\t\tcolor: var(--header-color);\n\t\t\t\t}\n\n\t\t\t\tbody > pre {\n\t\t\t\t\twhite-space: pre-wrap;\n\t\t\t\t\tcolor: var(--text-color);\n\t\t\t\t}\n\t\t\t</style></head><body><h3>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		var templ_7745c5c3_Var3 string
		templ_7745c5c3_Var3, templ_7745c5c3_Err = templ.JoinStringErrs(header)
		if templ_7745c5c3_Err != nil {
			return templ.Error{Err: templ_7745c5c3_Err, FileName: `templates.templ`, Line: 49, Col: 15}
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var3))
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</h3><pre>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		var templ_7745c5c3_Var4 string
		templ_7745c5c3_Var4, templ_7745c5c3_Err = templ.JoinStringErrs(message)
		if templ_7745c5c3_Err != nil {
			return templ.Error{Err: templ_7745c5c3_Err, FileName: `templates.templ`, Line: 50, Col: 17}
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var4))
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</pre>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		templ_7745c5c3_Err = jsInjection(printDebugLogs, wsEventsEndpoint).Render(ctx, templ_7745c5c3_Buffer)
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("</body></html>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		if !templ_7745c5c3_IsBuffer {
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteTo(templ_7745c5c3_W)
		}
		return templ_7745c5c3_Err
	})
}

func jsInjection(printDebugLogs bool, wsEventsEndpoint string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, templ_7745c5c3_W io.Writer) (templ_7745c5c3_Err error) {
		templ_7745c5c3_Buffer, templ_7745c5c3_IsBuffer := templ_7745c5c3_W.(*bytes.Buffer)
		if !templ_7745c5c3_IsBuffer {
			templ_7745c5c3_Buffer = templ.GetBuffer()
			defer templ.ReleaseBuffer(templ_7745c5c3_Buffer)
		}
		ctx = templ.InitializeContext(ctx)
		templ_7745c5c3_Var5 := templ.GetChildren(ctx)
		if templ_7745c5c3_Var5 == nil {
			templ_7745c5c3_Var5 = templ.NopComponent
		}
		ctx = templ.ClearChildren(ctx)
		templ_7745c5c3_Err = templ.JSONScript("_templier__jsInjection", struct {
			PrintDebugLogs   bool
			WSEventsEndpoint string
		}{
			PrintDebugLogs:   printDebugLogs,
			WSEventsEndpoint: wsEventsEndpoint,
		}).Render(ctx, templ_7745c5c3_Buffer)
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString("<script type=\"text/javascript\">\n\t\t// This script is injected by the templier proxy for automatic reload\n\t\tconst params = JSON.parse(\n\t\t\tdocument.getElementById('_templier__jsInjection').textContent\n\t\t)\n\n\t\tlet reloadIndicator\n\t\tfunction showLoadingIndicator() {\n\t\t\tif (reloadIndicator != null) {\n\t\t\t\treturn\n\t\t\t}\n\t\t\tdisplayingLoadingIndicator = true\n\t\t\treloadIndicator = document.createElement('p')\n\t\t\treloadIndicator.innerHTML = 'Reloading...'\n\t\t\treloadIndicator.style.top = 0\n\t\t\treloadIndicator.style.left = 0\n\t\t\treloadIndicator.style.width = '100%'\n\t\t\treloadIndicator.style.padding = '.25rem'\n\t\t\treloadIndicator.style.position = 'fixed'\n\t\t\treloadIndicator.style.background = 'rgba(0,0,0,.75)'\n\t\t\treloadIndicator.style.color = 'white'\n\t\t\tdocument.body.appendChild(reloadIndicator)\n\t\t}\n\t\tfunction hideLoadingIndicator() {\n\t\t\tif (reloadIndicator == null) {\n\t\t\t\treturn\n\t\t\t}\n\t\t\treloadIndicator.remove()\n\t\t\treloadIndicator = null\n\t\t}\n\n\t\tlet reconnectingOverlay \n\t\tfunction showReconnecting() {\n\t\t\tif (reconnectingOverlay != null) {\n\t\t\t\treturn\n\t\t\t}\n\t\t\treconnectingOverlay = document.createElement('p')\n\t\t\treconnectingOverlay.innerHTML = '🔌 reconnecting Templier...'\n\t\t\treconnectingOverlay.style.position = 'fixed'\n\t\t\treconnectingOverlay.style.top = 0\n\t\t\treconnectingOverlay.style.left = 0\n\t\t\treconnectingOverlay.style.fontSize = '1.25rem'\n\t\t\treconnectingOverlay.style.width = '100%'\n\t\t\treconnectingOverlay.style.height = '100%'\n\t\t\treconnectingOverlay.style.padding = '2rem'\n\t\t\treconnectingOverlay.style.textAlign = 'center'\n\t\t\treconnectingOverlay.style.background = 'rgba(0,0,0,.8)'\n\t\t\treconnectingOverlay.style.color = 'white'\n\t\t\tdocument.body.appendChild(reconnectingOverlay)\n\t\t}\n\t\tfunction hideReconnecting() {\n\t\t\tif (reconnectingOverlay == null) {\n\t\t\t\treturn\n\t\t\t}\n\t\t\treconnectingOverlay.remove()\n\t\t\treconnectingOverlay = null\n\t\t}\n\n\t\tconst protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'\n\n\t\tfunction connectWebsocket() {\n\t\t\tconst wsURL = `${protocol}//${window.location.host}${params.WSEventsEndpoint}`\n\t\t\tws = new WebSocket(wsURL)\n\t\t\tws.onopen = function (e) {\n\t\t\t\thideReconnecting()\n\t\t\t\thideLoadingIndicator()\n\t\t\t\tif (params.PrintDebugLogs) {\n\t\t\t\t\tconsole.debug('templier: connectWebsocket connected')\n\t\t\t\t}\n\t\t\t}\n\t\t\tif (params.PrintDebugLogs) {\n\t\t\t\tws.onerror = function (e) {\n\t\t\t\t\tconsole.debug('templier: websocket connection error: ' + e.data)\n\t\t\t\t}\n\t\t\t}\n\t\t\tws.onmessage = function (e) {\n\t\t\t\tswitch (e.data) {\n\t\t\t\tcase 'r': // Reload\n\t\t\t\t\twindow.location.reload()\n\t\t\t\tcase 'ri': // Reload initiated\n\t\t\t\t\tshowLoadingIndicator()\n\t\t\t\t\treturn\n\t\t\t\tcase 's': // Shutdown\n\n\t\t\t\t}\n\t\t\t}\n\t\t\tws.onclose = function (e) {\n\t\t\t\tshowReconnecting()\n\t\t\t\thideLoadingIndicator()\n\t\t\t\tif (params.PrintDebugLogs) {\n\t\t\t\t\tconsole.debug('templier: websocket disconnected, reconnecting...')\n\t\t\t\t}\n\t\t\t\tsetTimeout(() => connectWebsocket(), 300)\n\t\t\t}\n\t\t}\n\n\t\tconnectWebsocket()\n\n\t</script>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		if !templ_7745c5c3_IsBuffer {
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteTo(templ_7745c5c3_W)
		}
		return templ_7745c5c3_Err
	})
}