const $ = id => document.getElementById(id);
const main = $("main");
const html = value => String(value ?? "").replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const button = (route, label, secondary = false) => `<a class="button-link${secondary ? " secondary" : ""}" href="#${route}">${html(label)}</a>`;
const back = '<a class="back" href="#home">← Back to overview</a>';
const intro = (section, title, copy) => `<div class="intro"><span class="eyebrow">${section}</span><h1>${title}</h1><p class="muted">${copy}</p></div>`;
const pages = ["home","slots","upload","download","target","review","device","maintenance","backup","firmware","job"];
let device = null, devicePending = null, deviceError = "";
const receiverModel = () => /^(dm900|dm920|dm7080|dm820|dm520|dm525|dm520mini)$/.test(device?.Model) ? device.Model.toUpperCase() : "Dreambox";
const internalNAND = () => /^(dm520|dm525|dm520mini)$/.test(device?.Model);
let groups = null, catalog = null, catalogError = "", catalogPending = false;
let firmware = null, firmwareError = "", firmwarePending = false;
let job = {State:"idle"}, transfer = null, pending = false, restarting = false, online = false;
let selected = null, prepared = "", localError = "", pollPending = false, generation = 0;
const page = () => pages.includes(location.hash.slice(1)) ? location.hash.slice(1) : location.hash === "#install" ? "upload" : "home";
const slots = () => device?.Layout?.Slots || [];
const single = () => Boolean(device?.Layout && !device.Layout.Multi);
const slotName = id => single() ? "Internal image" : `Slot ${id}`;
const blocked = () => pending || Boolean(transfer) || ["working","kernel-choice","reboot"].includes(job.State);
const multibootHint = () => device?.Layout?.Multi ? '<aside class="slot-advice"><strong>For reliable multiboot, install OpenATV 8 or newer in slot 1.</strong><p>Slot 1 supplies the kernel and drivers for every slot. To use DreamOS alongside OpenATV, install DreamOS in slot 2 or higher.</p></aside>' : "";
const disabled = flag => flag ? " disabled" : "";
function navigate(name) {
	if (page() === name) render(); else location.hash = name;
}
function notice(message) {
	$("notice").textContent = message;
	clearTimeout(notice.timer);
	notice.timer = setTimeout(() => { $("notice").textContent = ""; }, 6500);
}
async function api(path, data) {
	const response = await fetch(`/api/${path}`, data === undefined ? {cache:"no-store"} : {
		method:"POST", headers:{"Content-Type":"application/json","X-ODR-Request":"1"}, body:JSON.stringify(data)
	});
	if (!response.ok) {
		const error = new Error((await response.text()).trim() || `Request failed (${response.status})`);
		error.status = response.status;
		throw error;
	}
	return response.json();
}
function connection(ok) {
	online = ok;
	$("connection").textContent = ok ? "Connected" : restarting ? "Restarting…" : "Reconnecting…";
	$("connection").classList.toggle("offline", !ok);
}
function info() {
	if (devicePending) return devicePending;
	devicePending = api("info").then(data => { device = data; deviceError = ""; }).catch(error => {
		deviceError = error.message;
	}).finally(() => { devicePending = null; });
	return devicePending;
}
function storageWarning() {
	const reason = device?.LayoutError || device?.ImageWarning || deviceError;
	return reason ? `<p class="warning" role="status">${html(reason)}</p>` : "";
}
function strip() {
	const count = slots().length;
	return `<div class="device-strip"><div><strong>Dreambox ${receiverModel()}</strong><span>ODR ${html(device?.Version || "1.0")}</span></div><div><strong>${html(device?.Layout?.Profile || "Internal storage")}</strong><span>${count ? single() ? "One image" : `${count} slots` : "Layout unavailable"}</span></div><div><span>${html(device?.IP || "")}</span></div></div>`;
}
function slotCards(choose) {
	return `<div class="slots">${slots().map(s => {
		const isSelected = choose ? selected === s.ID : device.Layout.Selected === s.ID;
		const detail = [device.Layout.Multi && s.ID === 1 ? "Shared kernel · OpenATV 8+" : "", single() ? s.Label : "", s.Incomplete ? "Installation incomplete — reinstall before booting" : s.Installed ? "Installed" : "Empty or unreadable", device.Layout.Selected === s.ID ? "Selected for next boot" : ""].filter(Boolean).join(" · ");
		const title = single() ? "Internal image" : s.Label || slotName(s.ID);
		return `<div class="slot${isSelected ? " selected" : ""}">${internalNAND() ? "" : `<span class="slot-number">${s.ID}</span>`}<div class="slot-copy"><strong>${html(title)}</strong><small>${html(detail)}</small></div><button type="button" class="secondary" data-act="${choose ? "select" : "start"}" data-slot="${s.ID}"${choose ? ` aria-pressed="${isSelected}"` : ""}${disabled(blocked() || (!choose && !s.Installed))}>${choose ? isSelected ? "Selected" : "Select" : "Start"}</button></div>`;
	}).join("") || '<p class="muted">No image destination is available.</p>'}</div>`;
}
function home() {
	return `${intro("Open Dream Rescue","Recover your Dreambox.",internalNAND() ? "Start or replace the image in internal NAND." : "Start an installed image or load a new image and choose where to install it.")}${strip()}${storageWarning()}
	<div class="grid"><article class="card task featured"><span class="icon" aria-hidden="true">↗</span><h2>${single() ? "Start internal image" : "Start a slot"}</h2><p>Boot an image already installed on your box.</p>${internalNAND() ? `<button data-act="start" data-slot="1"${disabled(blocked() || !slots()[0]?.Installed)}>Start internal image</button>` : button("slots","Choose image")}</article>
	<article class="card task"><span class="icon" aria-hidden="true">↑</span><h2>Upload an image</h2><p>Transfer an image file from your computer or phone.</p>${button("upload","Choose file",true)}</article>
	<article class="card task"><span class="icon" aria-hidden="true">↓</span><h2>Download an image</h2><p>Choose a ${receiverModel()} image from the online catalogue.</p>${button("download","Browse images",true)}</article></div>
	<section><div class="section-head"><h2>Installed images</h2><span class="tag neutral">${single() ? "Internal image" : `${slots().length} slots`}</span></div>${multibootHint()}${slotCards(false)}</section>
	<details class="card panel" id="hardware"><summary>Device information</summary><div id="device-rows"></div></details>`;
}
function taskSteps(step) {
	return `<ol class="steps" aria-label="Installation steps">${["Load image",single() ? "Internal image" : "Choose destination","Review & install"].map((text,i) =>
		`<li${i+1 === step ? ' aria-current="step"' : ""}><b>0${i+1}</b>${text}</li>`).join("")}</ol>`;
}
function loadPage(download) {
	const card = download ? `<h2>Online catalogue</h2><p class="muted">Image sources from oe-alliance/bootmenu for the ${receiverModel()}.</p><div id="catalogue"></div>
	<details class="source-details"><summary>Use a direct image link</summary><form id="direct"><label class="form-row">HTTPS image URL<input type="url" name="url" required placeholder="https://"></label><div class="actions"><button${disabled(blocked())}>Download image</button></div></form></details>` :
	`<h2>Image from your computer</h2><label class="dropzone"><strong>Choose a ${receiverModel()} image file</strong><span class="muted">Community ZIP or compressed DreamOS root filesystem</span><input id="file" type="file" accept=".zip,.xz,.bz2,.gz,.tar" aria-label="Choose image file"></label><div class="actions"><button data-act="upload" id="upload" disabled>Upload image</button></div>`;
	return `${back}${intro("Install an image",download ? "Download an image." : "Upload an image.",internalNAND() ? "Load a DM520-family image and review it before replacing the internal image." : "Choose a destination after loading the image. ODR verifies the root filesystem during installation.")}<section class="panel">${taskSteps(1)}<div class="card">${card}</div></section>`;
}
function catalogueView() {
	const box = $("catalogue");
	if (!box) return;
	if (catalogError) { box.innerHTML = `<p class="warning">${html(catalogError)}</p><button class="secondary" data-act="reload-catalog">Retry catalogue</button>`; return; }
	if (!catalog) { box.textContent = "Loading catalogue…"; return; }
	box.innerHTML = catalog.length ? `<form id="download"><div class="catalog">${catalog.map((entry,i) =>
		`<label><input type="radio" name="entry" value="${i}"${i === 0 ? " checked" : ""}><span><strong>${html(entry.Name.replaceAll("_"," "))}</strong>${job.State === "error" && job.Action === "download" && job.DownloadIssue && entry.Key && job.DownloadKey === entry.Key ? `<br><small>Last attempt: ${html(job.DownloadIssue)}</small>` : ""}</span></label>`).join("")}</div><div class="actions"><button${disabled(blocked())}>Download image</button></div></form>` : '<p>No images are listed for this model.</p>';
}
async function loadCatalogue() {
	catalogueView();
	if (catalog || catalogPending || catalogError) return;
	catalogPending = true;
	try { catalog = await api("catalog"); } catch (error) { catalogError = error.message; }
	finally { catalogPending = false; catalogueView(); }
}
function targetPage(review) {
	if (job.State !== "ready" || !job.ImageID) return `${back}<div class="card panel"><h1>Load an image first.</h1><div class="actions">${button("upload","Upload image")}${button("download","Browse downloads",true)}</div></div>`;
	if (internalNAND()) { selected = 1; review = true; }
	const s = slots().find(slot => slot.ID === selected);
	if (internalNAND() && !s) return back + storageWarning();
	if (!s) review = false;
	const name = job.Image || `${receiverModel()} image`;
	const dreamSlot = device?.Layout?.Multi && s?.ID === 1 && /^(?:dreambox-image-deb-|dreamos(?:[-_.]|$))/i.test(name);
	const legacyAdvice = dreamSlot ? '<aside class="slot-advice legacy-advice" role="alert"><strong>DreamOS in slot 1 disables multiboot.</strong><p>Slots 2 and higher will no longer boot. Choose a guest slot to keep multiboot, or continue to review the single-image installation.</p></aside>' : "";
	let body;
	if (review) {
		body = `<div class="card"><h2>Review your selection</h2><div class="row"><span>Image</span><strong>${html(name)}</strong></div>
		<div class="row"><span>Destination</span><strong>${html(slotName(s.ID))}${s.Label ? ` · ${html(s.Label)}` : ""}</strong></div>
		${legacyAdvice}<p class="warning">${single() && !s.Folder ? "The internal root partition will be formatted." : `The current contents of ${single() ? "the internal image" : `slot ${s.ID}`} will be erased first.`} If flashing fails, reinstall the image before booting it.</p>
		<label class="check"><input type="checkbox" id="overwrite">I want to replace the contents of this destination.</label><div class="actions">${internalNAND() ? "" : button("target","Change destination",true)}<button data-act="install" id="install" disabled>Install image</button></div></div>`;
	} else {
		body = `<div class="summary"><strong>${html(name)}</strong><p class="small">Image loaded · Dreambox ${receiverModel()}</p></div><h2>Where should the image go?</h2>${storageWarning()}${slotCards(true)}${legacyAdvice}<div class="actions">${button("upload","Different image",true)}<button data-act="review"${disabled(!s || blocked())}>Review selection</button></div>`;
	}
	return `${back}${intro("Install an image","Install the loaded image.",internalNAND() ? "Confirm replacement of the internal NAND image." : "Choose the destination and confirm the installation.")}<section class="panel">${taskSteps(review ? 3 : 2)}${multibootHint()}${body}</section>`;
}
function maintenance() {
	return `${back}${intro("Maintenance","Manage your Dreambox.","Inspect the receiver and manage its installed images.")}${storageWarning()}<div class="maintenance">
	<article class="card"><span class="icon" aria-hidden="true">ⓘ</span><h2>Device information</h2><p>View serial numbers, system, storage and network information.</p>${button("device","View device information",true)}</article>
	<article class="card"><span class="icon" aria-hidden="true">↓</span><h2>Backups</h2><p>Save a complete image or just its receiver settings to your computer or phone.</p>${button("backup","Back up an image or settings",true)}</article>
	<article class="card"><span class="icon" aria-hidden="true">↻</span><h2>Device firmware</h2><p>Check for an update to the internal ${receiverModel()} rescue loader.</p>${button("firmware","Rescue updates",true)}</article>
	${device?.Layout?.Multi ? `<article class="card"><span class="icon" aria-hidden="true">▤</span><h2>Remove multiboot</h2><p>Keep slot 1 as the internal image. Remove the configured guest images and the STARTUP files.</p><button class="secondary" data-act="remove"${disabled(blocked())}>Remove multiboot…</button></article>` : ""}
	</div>`;
}
function firmwarePage() {
	const rel = firmware?.Release;
	let status = `Checking the ${receiverModel()} rescue catalogue…`;
	if (firmwareError) status = firmwareError;
	else if (firmware) status = !rel?.Version ? `No rescue update has been published for the ${receiverModel()} yet.` : firmware.Installed ? "This build is already installed in the internal rescue." : "An update is available for the internal rescue.";
	const update = rel?.Version && !firmware.Installed;
	return `${back}${intro("Maintenance","Device firmware.",`Update the internal recovery environment on your Dreambox ${receiverModel()}.`)}<section class="card panel"><h2>Open Dream Rescue</h2>
	<p class="${firmwareError ? "warning" : "hint"}" role="status">${html(status)}</p>
	${rel?.Version ? `<p>Published version <strong>${html(rel.Version)}</strong> · ${(rel.Bytes/1048576).toFixed(2)} MiB</p><details class="job-details"><summary>Build details</summary><p>SHA-256\n${html(rel.SHA256)}</p></details>` : ""}
	<div class="actions"><button class="secondary" data-act="check-firmware"${disabled(firmwarePending || blocked())}>Check again</button>${update ? `<button data-act="rescue-update"${disabled(blocked())}>Update internal rescue…</button>` : ""}</div></section>`;
}
async function checkFirmware() {
	if (firmwarePending) return;
	firmwarePending = true; firmware = null; firmwareError = "";
	if (page() === "firmware") main.innerHTML = firmwarePage();
	try { firmware = await api("firmware?date="+encodeURIComponent(new Date().toISOString())); }
	catch (err) { firmwareError = err.message; }
	finally { firmwarePending = false; if (page() === "firmware") main.innerHTML = firmwarePage(); }
}
function backupPage() {
	const installed = slots().filter(s => s.Installed);
	const latest = job.Action === "backup" && job.BackupID && ["done","backup-download"].includes(job.State) ? button("job","Download backup",true) : "";
	return `${back}${intro("Maintenance","Back up an image or settings.","Choose an installed image. The receiver reads it and prepares a file for download.")}${storageWarning()}
	<form class="card panel" id="backup-form">${internalNAND() ? '<p>Source: Internal NAND image</p><input type="hidden" name="slot" value="1">' : `<label class="form-row">Source image<select name="slot" required>${installed.map(s => `<option value="${s.ID}"${s.ID === device.Layout.Selected ? " selected" : ""}>${html(slotName(s.ID))}${s.Label ? " · "+html(s.Label) : ""}</option>`).join("")}</select></label>`}
	<fieldset class="backup-kinds"><legend>What do you want to save?</legend>
	<label class="check"><input type="radio" name="kind" value="image" checked><span><strong>Complete image</strong><br>Kernel and root filesystem in a ZIP you can install with ODR.</span></label>
	<label class="check"><input type="radio" name="kind" value="settings"><span><strong>Settings only</strong><br>Receiver settings, bouquets and network configuration in a tar.gz archive.</span></label></fieldset>
	<p class="hint">Backups may contain passwords and private keys. Save the download somewhere private.</p>
	${job.State === "ready" ? '<p class="warning">Starting a backup discards the image currently prepared for installation.</p>' : ""}
	${!installed.length ? '<p class="warning">No readable installed image is available.</p>' : ""}
	<div class="actions">${latest}<button${disabled(blocked() || !installed.length)}>Create backup</button></div></form>`;
}
function deviceRows() {
	const box = $("device-rows");
	if (!box) return;
	if (!groups) { box.textContent = "Reading device information…"; return; }
	box.innerHTML = `<div class="device-groups">${groups.filter(g => g.Fields?.length).map(g => `<section><h3>${html(g.Title)}</h3>${g.Fields.map(f =>
		`<div class="row"><strong>${html(f.Label)}</strong><span>${html(f.Value)}</span></div>`).join("")}</section>`).join("")}</div><div class="actions"><button class="secondary" data-act="devices">Refresh information</button></div>`;
}
async function readDevice() {
	deviceRows();
	try { groups = await api("device"); deviceRows(); }
	catch (error) { if ($("device-rows")) $("device-rows").innerHTML = `<p class="warning">${html(error.message)}</p><button data-act="devices">Retry</button>`; }
}
function jobPage() {
	const active = transfer || job.State === "working";
	let title, message, actions = button("home","Back to overview",true);
	const failedFlash = job.State === "error" && job.Action === "install" && job.DestinationAtRisk;
	if (failedFlash) {
		title = "Flash failed — do not boot this image";
		message = `ODR started erasing ${slotName(job.Slot).toLowerCase()}. It may be incomplete and unable to boot. Install a complete image in this destination before trying to start it.`;
		actions = button("upload","Upload a replacement image")+button("download","Browse downloads",true)+actions;
	}
	else if (job.State === "error" && job.Action === "download" && job.DownloadIssue) {
		title = job.DownloadIssue;
		message = job.Message;
		actions = button("download","Choose another download")+button("upload","Upload an image",true);
	}
	else if (localError || job.State === "error") { title = "Operation failed"; message = localError || job.Message; }
	else if (restarting && !online) { title = "Restart requested"; message = "The rescue connection has closed. Follow startup on your TV."; }
	else if (active) { title = transfer ? "Uploading image" : job.Transfer && job.Action !== "rescue-update" ? (job.Action === "download" ? "Downloading image" : "Uploading image") : job.Message;
		message = transfer ? "Keep this browser tab open until the transfer finishes." : "You can return to this page to check the operation."; }
	else if (job.State === "ready") { title = "Image loaded"; message = `${job.Image || "Your image"} is ready. Choose where to install it.`; if (internalNAND()) message = "Your image is ready for installation in internal NAND."; actions = (internalNAND() ? button("review","Review installation") : button("target","Choose destination"))+actions; }
	else if (job.State === "kernel-choice") {
		title = "Install slot 1 as a single image?";
		message = `${job.Message} Continuing installs the image kernel and removes the STARTUP configuration. Slots 2 and higher will no longer boot. Their images stay on disk. To keep multiboot, cancel and use OpenATV 8 or newer in slot 1.`;
		actions = '<button class="secondary" data-act="cancel-kernel">Cancel and keep multiboot</button><button class="danger" data-act="allow-kernel">Disable multiboot and install…</button>';
	}
	else if (job.State === "done" && job.Action === "install") { title = "Image installed successfully"; message = `Your new image is ready in ${slotName(job.Slot).toLowerCase()}.`;
		actions = `<button data-act="start" data-slot="${job.Slot}">Start new image</button>`+actions; }
	else if (job.Action === "backup" && ["done","backup-download"].includes(job.State)) { title = job.State === "backup-download" ? "Download your backup" : job.BackupID ? "Backup ready" : "Backup sent"; message = job.Message;
		if (job.BackupID) actions = `<a class="button-link" href="/api/backup-file?id=${encodeURIComponent(job.BackupID)}" download>Download backup</a>`+actions; }
	else if (job.State === "done" && job.Action === "rescue-update") { title = "Internal rescue verified"; message = job.Message; }
	else if (job.State === "done") { title = "Operation completed"; message = job.Message; }
	else if (restarting || job.State === "reboot") { title = "Dreambox is restarting"; message = "The rescue connection closes while the receiver boots. Follow startup on your TV."; }
	else { title = "No active operation"; message = "Choose an image on the overview to get started."; }
	const meter = active && !localError && !(restarting && !online) ? '<div class="job-progress"><div class="progress-heading"><strong id="phase"></strong><span id="progress-percent"></span></div><progress id="meter" max="100" aria-label="Operation progress"></progress><div id="amount" class="small muted"></div></div>' : "";
	const lines = transfer ? [transfer.name,transfer.sent ? "Checking the uploaded image" : "Uploading image"] : [
		job.Image, job.Slot ? slotName(job.Slot) : "", job.Details || job.Message, job.Filename, localError !== job.Message ? localError : ""
	];
	const open = $("job-details")?.open || ((localError || job.State === "error") && !$("job-details")?.dataset.error);
	const details = (job.State !== "idle" || transfer || localError) ? `<details id="job-details" class="job-details"${open ? " open" : ""}${(localError || job.State === "error") ? ' data-error="1"' : ""}><summary>Details</summary><p>${html(lines.filter(Boolean).join("\n"))}</p></details>` : "";
	return `${back}<section class="card panel${failedFlash ? " failed-flash" : ""}"><h1>${html(title)}</h1><p role="${failedFlash ? "alert" : "status"}">${html(message)}</p>${meter}${details}<div class="actions">${active && !localError ? "" : actions}</div></section>`;
}
function updateMeter() {
	const meter = $("meter");
	if (!meter) return;
	const uploading = transfer && !transfer.sent;
	const measuring = uploading || (!transfer && job.Transfer);
	const done = uploading ? transfer.done : job.Bytes, total = uploading ? transfer.total : job.Total;
	const mib = n => `${(n/1048576).toFixed(1)} MiB`;
	$("phase").textContent = uploading ? "Uploading image" : transfer ? "Checking image on the receiver" : job.Transfer && job.Action !== "rescue-update" ? (job.Action === "download" ? "Downloading image" : "Uploading image") : job.Message;
	const percent = measuring && total > 0 ? Math.min(100,done*100/total) : null;
	if (percent === null) meter.removeAttribute("value"); else meter.value = percent;
	$("progress-percent").textContent = percent === null ? "" : `${percent.toFixed(1)}%`;
	$("amount").textContent = measuring ? total > 0 ? `${mib(done)} of ${mib(total)}` : `${mib(done)} transferred` : "This step may take a few minutes.";
}
function chrome() {
	$("device-model").textContent = receiverModel();
	document.title = `Open Dream Rescue · ${receiverModel()}`;
	const banner = $("operation-banner");
	const active = transfer || ["working","kernel-choice","ready","reboot"].includes(job.State) || pending;
	banner.hidden = !active || page() === "job";
	banner.querySelector("span").textContent = transfer ? "Uploading image." : job.State === "ready" ? "Choose where to install the loaded image." : job.Message || "Operation in progress.";
	$("restart").disabled = blocked();
	if ($("upload")) $("upload").disabled = blocked() || !$("file").files.length;
	for (const form of main.querySelectorAll("#download, #direct")) form.querySelector("button").disabled = blocked();
}
function render() {
	const route = page();
	document.querySelectorAll("nav a").forEach(a => {
		if (a.hash === (["device","maintenance","backup","firmware"].includes(route) ? "#maintenance" : "#home")) a.setAttribute("aria-current","page"); else a.removeAttribute("aria-current");
	});
	chrome();
	switch (route) {
	case "home": main.innerHTML = home(); $("hardware").ontoggle = () => { if ($("hardware")?.open) readDevice(); }; break;
	case "slots": main.innerHTML = `${back}${intro("Start an image","Choose an installed image.","Select Start to confirm which image to boot.")}${multibootHint()}${storageWarning()}${slotCards(false)}`; break;
	case "upload": case "download": main.innerHTML = loadPage(route === "download"); if (route === "download") loadCatalogue(); break;
	case "target": case "review": main.innerHTML = targetPage(route === "review"); break;
	case "maintenance": main.innerHTML = maintenance(); break;
	case "backup": main.innerHTML = backupPage(); break;
	case "firmware": main.innerHTML = firmwarePage(); if (!firmware && !firmwareError && !blocked()) checkFirmware(); break;
	case "device": main.innerHTML = `${back}${intro("Maintenance","Device information.","Hardware and network details reported by this receiver.")}<div class="card panel" id="device-rows"></div>`; readDevice(); break;
	case "job": main.innerHTML = jobPage(); updateMeter(); break;
	}
}
function applyJob(next) {
	const previous = job;
	job = next;
	restarting = next.State === "reboot" || (next.State === "working" && ["start","restart"].includes(next.Action));
	if (next.ImageID !== prepared) { selected = null; prepared = next.ImageID || ""; }
	const changed = ["State","Message","Action","Slot","ImageID","Details","BackupID"].some(k => previous[k] !== next[k]);
	if (changed && next.Action === "rescue-update" && ["done","error"].includes(next.State)) { firmware = null; firmwareError = ""; }
	chrome();
	if (changed) {
		if (page() === "download" && next.Action === "download") catalogueView();
		if (page() === "job" || ["target","review","slots","maintenance","backup","firmware"].includes(page()) || page() === "home") render();
		if (["done","ready","error","idle"].includes(next.State) && (changed || !device)) refreshInventory();
	} else updateMeter();
}
async function refreshInventory() {
	const route = page();
	await info();
	if (page() === route && ["slots","target","home","maintenance","backup"].includes(route)) render();
}
async function poll() {
	if (pollPending || pending || transfer) return;
	pollPending = true;
	const epoch = generation;
	try {
		const next = await api("job");
		if (epoch !== generation) return;
		const recovered = !online;
		connection(true);
		applyJob(next);
		if (recovered && page() === "job") render();
		if (recovered && !device && !blocked()) refreshInventory();
	} catch {
		if (epoch !== generation) return;
		connection(false);
		if (page() === "job" && restarting) render();
		else if (page() === "job" && !localError) {
			main.innerHTML = `${back}<div class="card panel"><h1>Connection lost</h1><p>Waiting for the receiver. This page will reconnect and show the operation result.</p></div>`;
		}
	} finally { pollPending = false; }
}
function confirmation(title, text, label, danger = false) {
	const dialog = $("confirm-dialog");
	if (dialog.open) return Promise.resolve(false);
	$("dialog-title").textContent = title; $("dialog-body").textContent = text;
	$("dialog-accept").textContent = label; $("dialog-accept").classList.toggle("danger",danger);
	dialog.returnValue = "cancel";
	return new Promise(resolve => {
		dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), {once:true});
		dialog.showModal();
		dialog.querySelector('button[value="cancel"]').focus();
	});
}
async function post(action, data) {
	if (blocked()) return;
	localError = ""; pending = true; generation++; chrome();
	try {
		const result = await api(action,data);
		job = {State:"working",Action:action,Slot:data.Slot,Message:{start:"Starting image",restart:"Restarting Dreambox",download:"Contacting image server",install:"Installing image",remove:"Removing multiboot",backup:"Reading the backup source","rescue-update":"Checking the published rescue update"}[action]};
		restarting = action === "start" || action === "restart";
		navigate("job");
		if (action === "backup" && result?.BackupID) {
			const link = document.createElement("a");
			link.href = "/api/backup-file?id="+encodeURIComponent(result.BackupID);
			link.download = "";
			link.click();
		}
	} catch (error) {
		localError = operationError(error); navigate("job");
	} finally { pending = false; chrome(); await poll(); }
}
function operationError(error) {
	// A competing job owns the status page and will be picked up by poll().
	if (error.status === 409 && error.message === "Operation in progress") return "";
	return error.message;
}
async function upload(file) {
	if (!file || blocked()) return;
	generation++; localError = "";
	transfer = {name:file.name,total:file.size,done:0,sent:false};
	navigate("job");
	try {
		await new Promise((resolve,reject) => {
			const x = new XMLHttpRequest();
			x.open("POST","/api/upload"); x.setRequestHeader("X-ODR-Request","1"); x.setRequestHeader("X-ODR-Image",encodeURIComponent(file.name));
			x.upload.onprogress = event => { transfer.done = event.loaded; if (event.lengthComputable) transfer.total = event.total; updateMeter(); };
			x.upload.onload = () => { transfer.sent = true; updateMeter(); };
			x.onload = () => x.status === 200 ? resolve() : reject(Object.assign(Error(x.responseText.trim() || `Upload failed (${x.status})`),{status:x.status}));
			x.onerror = () => reject(Error("Upload connection lost. Reconnect and check the operation before retrying."));
			x.send(file);
		});
	} catch (error) { localError = operationError(error); }
	finally { transfer = null; await poll(); if (page() === "job") render(); }
}
async function action(name, id) {
	switch (name) {
	case "upload": return upload($("file").files[0]);
	case "reload-catalog": catalogError = ""; return loadCatalogue();
	case "devices": return readDevice();
	case "check-firmware": return checkFirmware();
	case "rescue-update": {
		const rel = firmware?.Release;
		if (!rel?.SHA256 || firmware.Installed || blocked()) return;
		if (await confirmation("Update internal rescue?",`Install Open Dream Rescue ${rel.Version} in the ${receiverModel()} SPI rescue partition? Keep the receiver powered until verification finishes.`,"Update rescue",true)) {
			return post(name,{Confirm:name,ImageID:rel.SHA256,Date:new Date().toISOString()});
		}
		break;
	}
	case "cancel-kernel": case "allow-kernel": {
		if (pending || job.State !== "kernel-choice" || !job.DecisionID) return;
		const decision = job.DecisionID, allow = name === "allow-kernel";
		if (allow && !await confirmation("Disable multiboot?","Install the image kernel and boot slot 1 as the only internal image. Slots 2 and higher will no longer start.","Disable multiboot and install",true)) return;
		pending = true;
		try { await api("kernel-decision",{DecisionID:decision,Confirm:allow ? "disable-multiboot" : "cancel"}); }
		catch (error) { notice(error.message); }
		finally { pending = false; await poll(); }
		break;
	}
	case "select": if (blocked()) return; selected = id; render(); main.querySelector(`[data-slot="${id}"]`)?.focus(); break;
	case "review": navigate("review"); break;
	case "install": if ($("overwrite")?.checked && selected && job.State === "ready") return post("install",{Slot:selected,Confirm:"install",ImageID:prepared}); break;
	case "start": if (await confirmation(`Start ${slotName(id)}?`,"The Dreambox will restart and boot this image.","Start image")) return post("start",{Slot:id,Confirm:"start"}); break;
	case "restart": if (await confirmation("Restart Dreambox?","The receiver will restart with its current boot selection.","Restart")) return post("restart",{Confirm:"restart"}); break;
	case "remove": if (await confirmation("Remove multiboot?","Keep slot 1 and its shared kernel. Permanently delete the configured guest images and all STARTUP files.","Remove multiboot",true)) return post("remove",{Confirm:"remove"}); break;
	}
}
document.addEventListener("click",event => {
	const el = event.target.closest("[data-act], #restart");
	if (el && !el.disabled) action(el.dataset.act || "restart", Number(el.dataset.slot)).catch(e => notice(e.message));
});
main.addEventListener("change",event => {
	if (event.target.id === "file") $("upload").disabled = blocked() || !event.target.files.length;
	if (event.target.id === "overwrite") $("install").disabled = blocked() || !event.target.checked;
});
main.addEventListener("submit",event => {
	event.preventDefault();
	const form = event.target;
	const data = new FormData(form);
	if (form.id === "backup-form") {
		const now = new Date(), date = new Date(now.getTime()-now.getTimezoneOffset()*60000).toISOString().slice(0,10);
		post("backup",{Slot:Number(data.get("slot")),Kind:data.get("kind"),Date:date}).catch(e => notice(e.message));
		return;
	}
	const entry = form.id === "download" ? catalog?.[Number(data.get("entry"))] : null;
	const url = entry ? entry.URL : data.get("url")?.trim();
	try { if (new URL(url).protocol !== "https:") throw Error(); }
	catch { notice("Enter a valid HTTPS image URL."); return; }
	post("download",entry ? {URL:url,Name:entry.Name.replaceAll("_"," ")} : {URL:url}).catch(e => notice(e.message));
});
document.querySelector(".skip").onclick = event => { event.preventDefault(); main.focus(); };
window.addEventListener("hashchange",() => { render(); main.focus({preventScroll:true}); window.scrollTo(0,0); });
async function init() {
	try { job = await api("job"); connection(true); prepared = job.ImageID || ""; restarting = job.State === "reboot"; }
	catch { connection(false); }
	if (!blocked()) await info();
	render();
	setInterval(poll,1200);
}
init();
