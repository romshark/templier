// Code generated by templ - DO NOT EDIT.

// templ: version: v0.3.924
package server

//lint:file-ignore SA4006 This context is only used if a nested component is present.

import "github.com/a-h/templ"
import templruntime "github.com/a-h/templ/runtime"

func pageError(
	title string,
	content []Report,
	printDebugLogs bool, wsEventsEndpoint string,
) templ.Component {
	return templruntime.GeneratedTemplate(func(templ_7745c5c3_Input templruntime.GeneratedComponentInput) (templ_7745c5c3_Err error) {
		templ_7745c5c3_W, ctx := templ_7745c5c3_Input.Writer, templ_7745c5c3_Input.Context
		if templ_7745c5c3_CtxErr := ctx.Err(); templ_7745c5c3_CtxErr != nil {
			return templ_7745c5c3_CtxErr
		}
		templ_7745c5c3_Buffer, templ_7745c5c3_IsBuffer := templruntime.GetBuffer(templ_7745c5c3_W)
		if !templ_7745c5c3_IsBuffer {
			defer func() {
				templ_7745c5c3_BufErr := templruntime.ReleaseBuffer(templ_7745c5c3_Buffer)
				if templ_7745c5c3_Err == nil {
					templ_7745c5c3_Err = templ_7745c5c3_BufErr
				}
			}()
		}
		ctx = templ.InitializeContext(ctx)
		templ_7745c5c3_Var1 := templ.GetChildren(ctx)
		if templ_7745c5c3_Var1 == nil {
			templ_7745c5c3_Var1 = templ.NopComponent
		}
		ctx = templ.ClearChildren(ctx)
		templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 1, "<!doctype html><html lang=\"en\"><head><meta charset=\"UTF-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\"><title>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		var templ_7745c5c3_Var2 string
		templ_7745c5c3_Var2, templ_7745c5c3_Err = templ.JoinStringErrs(title)
		if templ_7745c5c3_Err != nil {
			return templ.Error{Err: templ_7745c5c3_Err, FileName: `internal/server/templates.templ`, Line: 13, Col: 17}
		}
		_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var2))
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 2, "</title><style>\n\t\t\t\t:root {\n\t\t\t\t\t--background-color: rgb(0,0,0);\n\t\t\t\t\t--text-color: coral;\n\t\t\t\t\t--header-color: red;\n\t\t\t\t}\n\n\t\t\t\t@media (prefers-color-scheme: light) {\n\t\t\t\t\t:root {\n\t\t\t\t\t\t--background-color: rgb(255,255,255);\n\t\t\t\t\t\t--text-color: orangered;\n\t\t\t\t\t\t--header-color: red;\n\t\t\t\t\t}\n\t\t\t\t}\n\n\t\t\t\tbody {\n\t\t\t\t\tfont-family: monospace;\n\t\t\t\t\tbox-sizig: border-box;\n\t\t\t\t\tmargin: 0;\n\t\t\t\t\tpadding: 1rem;\n\t\t\t\t\tbackground-color: var(--background-color);\n\t\t\t\t\tcolor: var(--text-color);\n\t\t\t\t}\n\n\t\t\t\tbody > h3 {\n\t\t\t\t\tmargin-top: 0;\n\t\t\t\t\tcolor: var(--header-color);\n\t\t\t\t}\n\n\t\t\t\tbody > pre {\n\t\t\t\t\twhite-space: pre-wrap;\n\t\t\t\t\tcolor: var(--text-color);\n\t\t\t\t}\n\t\t\t</style></head><body>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		for i, c := range content {
			templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 3, "<h3>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var3 string
			templ_7745c5c3_Var3, templ_7745c5c3_Err = templ.JoinStringErrs(c.Subject)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `internal/server/templates.templ`, Line: 51, Col: 19}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var3))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 4, "</h3><pre>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			var templ_7745c5c3_Var4 string
			templ_7745c5c3_Var4, templ_7745c5c3_Err = templ.JoinStringErrs(c.Body)
			if templ_7745c5c3_Err != nil {
				return templ.Error{Err: templ_7745c5c3_Err, FileName: `internal/server/templates.templ`, Line: 52, Col: 17}
			}
			_, templ_7745c5c3_Err = templ_7745c5c3_Buffer.WriteString(templ.EscapeString(templ_7745c5c3_Var4))
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 5, "</pre>")
			if templ_7745c5c3_Err != nil {
				return templ_7745c5c3_Err
			}
			if i+1 != len(content) {
				templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 6, "<hr>")
				if templ_7745c5c3_Err != nil {
					return templ_7745c5c3_Err
				}
			}
		}
		templ_7745c5c3_Err = jsInjection(printDebugLogs, wsEventsEndpoint).Render(ctx, templ_7745c5c3_Buffer)
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 7, "</body></html>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		return nil
	})
}

func jsInjection(printDebugLogs bool, wsEventsEndpoint string) templ.Component {
	return templruntime.GeneratedTemplate(func(templ_7745c5c3_Input templruntime.GeneratedComponentInput) (templ_7745c5c3_Err error) {
		templ_7745c5c3_W, ctx := templ_7745c5c3_Input.Writer, templ_7745c5c3_Input.Context
		if templ_7745c5c3_CtxErr := ctx.Err(); templ_7745c5c3_CtxErr != nil {
			return templ_7745c5c3_CtxErr
		}
		templ_7745c5c3_Buffer, templ_7745c5c3_IsBuffer := templruntime.GetBuffer(templ_7745c5c3_W)
		if !templ_7745c5c3_IsBuffer {
			defer func() {
				templ_7745c5c3_BufErr := templruntime.ReleaseBuffer(templ_7745c5c3_Buffer)
				if templ_7745c5c3_Err == nil {
					templ_7745c5c3_Err = templ_7745c5c3_BufErr
				}
			}()
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
		templ_7745c5c3_Err = templruntime.WriteString(templ_7745c5c3_Buffer, 8, "<script type=\"text/javascript\">\n\t\t(() => {\n\t\t\tif (window._templier__jsInjection_initialized === true) {\n\t\t\t\treturn\n\t\t\t}\n\t\t\tlet params = JSON.parse(\n\t\t\t\tdocument.getElementById('_templier__jsInjection').textContent\n\t\t\t)\n\n\t\t\tlet reconnectingOverlay \n\t\t\tfunction showReconnecting() {\n\t\t\t\tif (reconnectingOverlay != null) {\n\t\t\t\t\treturn\n\t\t\t\t}\n\t\t\t\treconnectingOverlay = document.createElement('p')\n\t\t\t\treconnectingOverlay.innerHTML = '<span>🔌 reconnecting Templier...</span>'\n\t\t\t\treconnectingOverlay.style.margin = 0\n\t\t\t\treconnectingOverlay.style.display = 'flex'\n\t\t\t\treconnectingOverlay.style.justifyContent = 'center'\n\t\t\t\treconnectingOverlay.style.alignItems = 'center'\n\t\t\t\treconnectingOverlay.style.position = 'fixed'\n\t\t\t\treconnectingOverlay.style.top = 0\n\t\t\t\treconnectingOverlay.style.left = 0\n\t\t\t\treconnectingOverlay.style.fontSize = '1.25rem'\n\t\t\t\treconnectingOverlay.style.width = '100%'\n\t\t\t\treconnectingOverlay.style.height = '100%'\n\t\t\t\treconnectingOverlay.style.background = 'rgba(0,0,0,.8)'\n\t\t\t\treconnectingOverlay.style.color = 'white'\n\t\t\t\tdocument.body.appendChild(reconnectingOverlay)\n\t\t\t}\n\t\t\tfunction hideReconnecting() {\n\t\t\t\tif (reconnectingOverlay == null) {\n\t\t\t\t\treturn\n\t\t\t\t}\n\t\t\t\treconnectingOverlay.remove()\n\t\t\t\treconnectingOverlay = null\n\t\t\t}\n\n\t\t\tconst protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'\n\n\t\t\tfunction connectWebsocket() {\n\t\t\t\tconst wsURL = `${protocol}//${window.location.host}${params.WSEventsEndpoint}`\n\t\t\t\tws = new WebSocket(wsURL)\n\t\t\t\tws.onopen = function (e) {\n\t\t\t\t\tif (reconnectingOverlay != null) {\n\t\t\t\t\t\twindow.location.reload()\n\t\t\t\t\t\treturn\n\t\t\t\t\t}\n\t\t\t\t\thideReconnecting()\n\t\t\t\t\tif (params.PrintDebugLogs) {\n\t\t\t\t\t\tconsole.debug('templier: connectWebsocket connected')\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t\tif (params.PrintDebugLogs) {\n\t\t\t\t\tws.onerror = function (e) {\n\t\t\t\t\t\tconsole.debug('templier: websocket connection error: ' + e.data)\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t\tws.onmessage = function (e) {\n\t\t\t\t\tswitch (e.data) {\n\t\t\t\t\tcase 'r': // Reload\n\t\t\t\t\t\twindow.location.reload()\n\t\t\t\t\tcase 's': // Shutdown\n\n\t\t\t\t\t}\n\t\t\t\t}\n\t\t\t\tws.onclose = function (e) {\n\t\t\t\t\tshowReconnecting()\n\t\t\t\t\tif (params.PrintDebugLogs) {\n\t\t\t\t\t\tconsole.debug('templier: websocket disconnected, reconnecting...')\n\t\t\t\t\t}\n\t\t\t\t\tsetTimeout(() => connectWebsocket(), 300)\n\t\t\t\t}\n\t\t\t}\n\n\t\t\tconnectWebsocket()\n\t\t\twindow._templier__jsInjection_initialized = true\n\t\t})();\n\t</script>")
		if templ_7745c5c3_Err != nil {
			return templ_7745c5c3_Err
		}
		return nil
	})
}

var _ = templruntime.GeneratedTemplate
