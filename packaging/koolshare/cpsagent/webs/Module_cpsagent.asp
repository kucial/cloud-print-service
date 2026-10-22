<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
<meta http-equiv="X-UA-Compatible" content="IE=Edge"/>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8" />
<meta HTTP-EQUIV="Pragma" CONTENT="no-cache"/>
<meta HTTP-EQUIV="Expires" CONTENT="-1"/>
<link rel="shortcut icon" href="images/favicon.png"/>
<link rel="icon" href="images/favicon.png"/>
<title>Software Center - Cloud Print Agent</title>
<link rel="stylesheet" type="text/css" href="index_style.css"/>
<link rel="stylesheet" type="text/css" href="form_style.css"/>
<link rel="stylesheet" type="text/css" href="usp_style.css"/>
<link rel="stylesheet" type="text/css" href="css/element.css">
<link rel="stylesheet" type="text/css" href="/res/softcenter.css">
<script type="text/javascript" src="/js/jquery.js"></script>
<script type="text/javascript" src="/state.js"></script>
<script type="text/javascript" src="/popup.js"></script>
<script type="text/javascript" src="/validator.js"></script>
<script type="text/javascript" src="/general.js"></script>
<script type="text/javascript" src="/res/softcenter.js"></script>
<style>
#cpsagent_log {
	width: 98%;
	height: 260px;
	font-family: monospace;
	font-size: 11px;
	background: #475a5f;
	color: #ffffff;
	border: 1px solid #222;
	padding: 4px;
}
.cps_hint {
	color: #FFCC00;
	margin-left: 8px;
}
.cps_warn {
	color: #FF6A6A;
}
</style>
<script>
var db_cpsagent = {};
var status_cpsagent = {};
var LAN_IP = '<% nvram_get("lan_ipaddr"); %>';

function init() {
	show_menu(menu_hook);
	set_skin();
	get_dbus_data();
	get_run_status();
	get_log();
}

function set_skin() {
	var SKN = '<% nvram_get("sc_skin"); %>';
	if (SKN) {
		$("#app").attr("skin", SKN);
	}
}

function menu_hook() {
	tabtitle[tabtitle.length - 1] = new Array("", "Cloud Print Agent", "__INHERIT__");
	tablink[tablink.length - 1] = new Array("", "Module_cpsagent.asp", "NULL");
}

function get_dbus_data() {
	$.ajax({
		type: "GET",
		url: "/_api/cpsagent_",
		dataType: "json",
		async: false,
		success: function(data) {
			db_cpsagent = data.result[0] || {};
			conf_to_obj();
		}
	});
}

function conf_to_obj() {
	E("cpsagent_enable").checked = db_cpsagent["cpsagent_enable"] == "1";
	E("cpsagent_server").value = db_cpsagent["cpsagent_server"] || "";
	E("cpsagent_id").value = db_cpsagent["cpsagent_id"] || "";
	E("cpsagent_site").value = db_cpsagent["cpsagent_site"] || "";
	E("cpsagent_host").value = db_cpsagent["cpsagent_host"] || "";
	E("cpsagent_host").placeholder = LAN_IP + " (LAN address)";
	E("cpsagent_base_port").value = db_cpsagent["cpsagent_base_port"] || "";
	if (db_cpsagent["cpsagent_version"]) {
		$("#cpsagent_version_show").text("Plugin " + db_cpsagent["cpsagent_version"]);
	}
}

function call_script(method, params, fields, done) {
	var id = parseInt(Math.random() * 100000000);
	var postData = {"id": id, "method": method, "params": params, "fields": fields};
	$.ajax({
		type: "POST",
		cache: false,
		url: "/_api/",
		data: JSON.stringify(postData),
		dataType: "json",
		success: function(response) { done(response.result == id || response.result == "ok", response.result); },
		error: function() { done(false, "request failed"); }
	});
}

function get_run_status() {
	call_script("cpsagent_status.sh", [], "", function(ok) {
		if (!ok) {
			setTimeout("get_run_status();", 5000);
			return;
		}
		$.ajax({
			url: "/_temp/cpsagent_status.txt",
			type: "GET",
			cache: false,
			dataType: "json",
			success: function(s) {
				status_cpsagent = s;
				show_status(s);
				setTimeout("get_run_status();", 10000);
			},
			error: function() { setTimeout("get_run_status();", 5000); }
		});
	});
}

function show_status(s) {
	if (s.running != 1) {
		$("#cps_status").text("Not running");
	} else if (s.pid) {
		$("#cps_status").text("Running (PID " + s.pid + ")");
	} else {
		$("#cps_status").text("Waiting for a printer, retrying every 30s (see the log)");
	}
	$("#cps_agent_version").text(s.version ? "cps-agent " + s.version : "");
	if (s.enrolled == 1) {
		$("#cps_enrollment").text("Enrolled as " + s.agent_id + (s.site ? " at site " + s.site : "") + " on " + s.server);
		$("#cps_reset_btn").show();
		$("#cps_token_hint").text("Only to move this router to another server or re-enroll it.");
	} else {
		$("#cps_enrollment").text("Not enrolled");
		$("#cps_reset_btn").hide();
		$("#cps_token_hint").text("Required: from cpsctl agent enroll on the server.");
	}
	$("#cps_dial").text(E("cpsagent_host").value || s.lan_ip);
	$("#cps_usb").text(s.usb || "No USB printer found");
	if (s.busy) {
		$("#cps_busy").text("The router's own printer server (" + s.busy + ") is running and may hold the printer. Turn off USB Application > Network Printer Server.").show();
	} else {
		$("#cps_busy").hide();
	}
}

function get_log() {
	$.ajax({
		url: "/_temp/cpsagent_log.txt",
		type: "GET",
		cache: false,
		dataType: "text",
		success: function(text) {
			var area = E("cpsagent_log");
			area.value = text;
			area.scrollTop = area.scrollHeight;
		},
		error: function() { E("cpsagent_log").value = "No log yet."; }
	});
}

function validate() {
	var server = E("cpsagent_server").value.trim();
	var port = E("cpsagent_base_port").value.trim();
	var host = E("cpsagent_host").value.trim();
	var token = E("cpsagent_enroll").value.trim();
	if (server && !/^https?:\/\/[^\s\/]+/.test(server)) {
		return "Server URL must start with http:// or https://";
	}
	if (port && !(/^\d+$/.test(port) && port >= 1024 && port <= 65000)) {
		return "First port must be between 1024 and 65000";
	}
	if (host && !/^[A-Za-z0-9.\-:]+$/.test(host)) {
		return "Address must be an IP address or host name";
	}
	if (E("cpsagent_enable").checked && status_cpsagent.enrolled != 1 && !token) {
		return "Enter an enrollment token to enroll this router";
	}
	if (token && !server) {
		return "Enter the server URL to enroll with";
	}
	return "";
}

function onSubmitCtrl() {
	var err = validate();
	if (err) {
		$("#cps_warning").text(err);
		return false;
	}
	$("#cps_warning").text("");
	var fields = {
		"cpsagent_enable": E("cpsagent_enable").checked ? "1" : "0",
		"cpsagent_server": E("cpsagent_server").value.trim(),
		"cpsagent_id": E("cpsagent_id").value.trim(),
		"cpsagent_site": E("cpsagent_site").value.trim(),
		"cpsagent_host": E("cpsagent_host").value.trim(),
		"cpsagent_base_port": E("cpsagent_base_port").value.trim()
	};
	var token = E("cpsagent_enroll").value.trim();
	if (token) {
		fields["cpsagent_enroll"] = token;
	}
	run_action(["apply"], fields, "Applying settings...");
}

function onResetCtrl() {
	if (!confirm("Remove this router's enrollment? It stops serving printers until it is enrolled again.")) {
		return false;
	}
	run_action(["reset"], "", "Removing the enrollment...");
}

function run_action(params, fields, message) {
	E("cmdBtn").disabled = true;
	$("#cps_warning").text(message);
	call_script("cpsagent_config.sh", params, fields, function(ok, result) {
		E("cmdBtn").disabled = false;
		E("cpsagent_enroll").value = "";
		$("#cps_warning").text(ok ? "" : "Failed: " + result);
		get_dbus_data();
		// Enrollment takes a few seconds; the log shows how it went.
		setTimeout("get_log();", 1500);
		setTimeout("get_log();", 6000);
	});
}

function reload_Soft_Center() {
	location.href = "/Module_Softcenter.asp";
}
</script>
</head>
<body id="app" skin="ASUSWRT" onload="init();">
	<div id="TopBanner"></div>
	<div id="Loading" class="popup_bg"></div>
	<table class="content" align="center" cellpadding="0" cellspacing="0">
		<tr>
			<td width="17">&nbsp;</td>
			<td valign="top" width="202">
				<div id="mainMenu"></div>
				<div id="subMenu"></div>
			</td>
			<td valign="top">
				<div id="tabMenu" class="submenuBlock"></div>
				<table width="98%" border="0" align="left" cellpadding="0" cellspacing="0">
					<tr>
						<td align="left" valign="top">
							<table width="760px" border="0" cellpadding="5" cellspacing="0" bordercolor="#6b8fa3" class="FormTitle" id="FormTitle" style="border: 0px solid transparent;">
								<tr>
									<td bgcolor="#4D595D" colspan="3" valign="top" style="border-radius: 8px">
										<div>&nbsp;</div>
										<div class="formfonttitle">Software Center - Cloud Print Agent</div>
										<div style="float:right; width:15px; height:25px;margin-top:-20px">
											<img id="return_btn" onclick="reload_Soft_Center();" align="right" style="cursor:pointer;position:absolute;margin-left:-30px;margin-top:-25px;" title="Back to Software Center" src="/images/backprev.png" onMouseOver="this.src='/images/backprevclick.png'" onMouseOut="this.src='/images/backprev.png'"></img>
										</div>
										<div style="margin:30px 0 10px 5px;" class="splitLine"></div>
										<div class="SimpleNote">
											<li>Serves the USB printer plugged into this router to Cloud Print Service.</li>
										</div>
										<table width="100%" border="1" align="center" cellpadding="4" cellspacing="0" bordercolor="#6b8fa3" class="FormTable">
											<thead>
												<tr>
													<td colspan="2">Status</td>
												</tr>
											</thead>
											<tr>
												<th>Enable</th>
												<td>
													<div class="switch_field" style="display:table-cell;float: left;">
														<label for="cpsagent_enable">
															<input id="cpsagent_enable" class="switch" type="checkbox" style="display: none;">
															<div class="switch_container">
																<div class="switch_bar"></div>
																<div class="switch_circle transition_style">
																	<div></div>
																</div>
															</div>
														</label>
													</div>
													<div id="cpsagent_version_show" style="padding-top:5px;margin-left:30px;float: left;"></div>
												</td>
											</tr>
											<tr>
												<th>Agent</th>
												<td><span id="cps_status">Checking...</span> <span id="cps_agent_version" class="cps_hint"></span></td>
											</tr>
											<tr>
												<th>Enrollment</th>
												<td>
													<span id="cps_enrollment">Checking...</span>
													<a type="button" class="ks_btn" id="cps_reset_btn" style="cursor:pointer;display:none;margin-left:10px" onclick="onResetCtrl();">Remove enrollment</a>
												</td>
											</tr>
											<tr>
												<th>Server dials</th>
												<td><span id="cps_dial"></span></td>
											</tr>
											<tr>
												<th>USB printers</th>
												<td>
													<span id="cps_usb">Checking...</span>
													<div id="cps_busy" class="cps_warn" style="display:none;margin-top:4px"></div>
												</td>
											</tr>
											<thead>
												<tr>
													<td colspan="2">Settings</td>
												</tr>
											</thead>
											<tr>
												<th>Server URL</th>
												<td>
													<input style="width:300px;" type="text" class="input_ss_table" id="cpsagent_server" maxlength="200" placeholder="http://192.168.50.10:8080" autocorrect="off" autocapitalize="off">
												</td>
											</tr>
											<tr>
												<th>Enrollment token</th>
												<td>
													<input style="width:300px;" type="password" class="input_ss_table" id="cpsagent_enroll" maxlength="200" autocomplete="new-password" autocorrect="off" autocapitalize="off" onBlur="switchType(this, false);" onFocus="switchType(this, true);">
													<div id="cps_token_hint" class="cps_hint" style="margin:4px 0 0 0"></div>
												</td>
											</tr>
											<tr>
												<th>Agent ID</th>
												<td>
													<input style="width:200px;" type="text" class="input_ss_table" id="cpsagent_id" maxlength="64" placeholder="router host name" autocorrect="off" autocapitalize="off">
													<span class="cps_hint">used when enrolling</span>
												</td>
											</tr>
											<tr>
												<th>Site</th>
												<td>
													<input style="width:200px;" type="text" class="input_ss_table" id="cpsagent_site" maxlength="64" autocorrect="off" autocapitalize="off">
													<span class="cps_hint">used when enrolling</span>
												</td>
											</tr>
											<tr>
												<th>Address for the server</th>
												<td>
													<input style="width:200px;" type="text" class="input_ss_table" id="cpsagent_host" maxlength="100" autocorrect="off" autocapitalize="off">
												</td>
											</tr>
											<tr>
												<th>First port</th>
												<td>
													<input style="width:80px;" type="text" class="input_ss_table" id="cpsagent_base_port" maxlength="5" placeholder="19100">
													<span class="cps_hint">one port per printer, counting up</span>
												</td>
											</tr>
										</table>
										<div id="cps_warning" class="cps_warn" style="font-size:14px;margin:20px auto;text-align:center"></div>
										<div class="apply_gen">
											<input class="button_gen" id="cmdBtn" onClick="onSubmitCtrl()" type="button" value="Apply" />
										</div>
										<div style="margin:30px 0 10px 5px;" class="splitLine"></div>
										<table width="100%" border="1" align="center" cellpadding="4" cellspacing="0" bordercolor="#6b8fa3" class="FormTable">
											<thead>
												<tr>
													<td>Log <a type="button" class="ks_btn" style="cursor:pointer;margin-left:10px" onclick="get_log();">Refresh</a></td>
												</tr>
											</thead>
											<tr>
												<td><textarea id="cpsagent_log" readonly="readonly" wrap="off"></textarea></td>
											</tr>
										</table>
									</td>
								</tr>
							</table>
						</td>
					</tr>
				</table>
			</td>
			<td width="10" align="center" valign="top"></td>
		</tr>
	</table>
	<div id="footer"></div>
</body>
</html>
