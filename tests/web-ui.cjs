const assert = require("node:assert/strict");
const {readFile} = require("node:fs/promises");
const {createServer} = require("node:http");
const {join} = require("node:path");
const {chromium} = require("playwright");

async function test() {
	const model = process.env.ODR_TEST_MODEL || "dm920";
	assert.ok(["dm7080","dm820","dm900","dm920"].includes(model));
	const browser = await chromium.launch({executablePath:process.env.ODR_BROWSER,headless:true});
	const assets = join(__dirname,"../backend/web");
	const server = createServer(async (req,res) => {
		if (nand && req.url === "/api/backup-file?id=test-stream") {
			job = {Action:"backup",State:"done",Message:"Backup sent."};
			res.writeHead(200,{"Content-Type":"application/octet-stream","Content-Disposition":"attachment; filename=dm520-test.zip"});
			res.end("test download");
			return;
		}
		const path = (req.url.split("?")[0] === "/" ? "/index.html" : req.url.split("?")[0]).slice(1);
		if (!["index.html","styles.css","app.js"].includes(path)) { res.writeHead(404).end(); return; }
		res.setHeader("Content-Type",{"index.html":"text/html; charset=utf-8","styles.css":"text/css","app.js":"application/javascript"}[path]);
		res.end(await readFile(join(assets,path)));
	});
	let job = {State:"idle"};
	const firmware = {Release:{},Installed:false};
	const slots = [1,2,3,4,5,6].map(ID => ({ID,Installed:ID < 5,Label:ID === 1 ? "openATV <8.1>" : ""}));
	const layout = {Multi:true,Profile:"Multiboot",Selected:1,Slots:slots};
	const actions = [], errors = [];
	let failStatus = false;
	let competingUpdate = false;
	let nand = false;
	try {
		await new Promise(resolve => server.listen(0,"127.0.0.1",resolve));
		const origin = `http://127.0.0.1:${server.address().port}`;
		const context = await browser.newContext({viewport:{width:360,height:800},colorScheme:"dark"});
		const p = await context.newPage();
		p.on("pageerror",e => errors.push(e.message));
		await p.route("**/api/*",async route => {
			const r = route.request(), name = new URL(r.url()).pathname.slice(5);
			if (r.method() === "GET") {
				if (name === "job" && failStatus) return route.abort();
				if (nand && name === "backup-file") {
					assert.equal(new URL(r.url()).searchParams.get("id"),"test-stream");
					return route.continue();
				}
				const replies = {job,firmware,info:{Model:nand ? "dm525" : model,IP:"192.168.0.30",Version:"1.0",Layout:layout},
					catalog:[{Name:"openATV_8.1",URL:"https://example.invalid/firmware.zip",Key:"missing81"},{Name:"openATV_8.0",URL:"https://example.invalid/8.0.zip",Key:"working80"}],
					device:[{Title:"System",Fields:[{Label:"SoC",Value:"BCM72521"}]}]};
				assert.ok(name in replies);
				return route.fulfill({json:replies[name]});
			}
			assert.equal(r.headers()["x-odr-request"],"1");
			if (name === "upload") {
				assert.equal(decodeURIComponent(r.headers()["x-odr-image"]),"openATV & friends.zip");
				job = {State:"ready",Action:name,Image:"openATV & friends.zip",ImageID:"staged-b"};
			} else {
				const args = r.postDataJSON();
				if (name === "rescue-update" && competingUpdate) {
					job = {State:"working",Action:name,Message:"Writing internal rescue"};
					return route.fulfill({status:409,contentType:"text/plain",body:"Operation in progress\n"});
				}
				actions.push([name,args]);
				if (nand && name === "backup") {
					job = {Action:name,State:"backup-download",BackupID:"test-stream",Slot:args.Slot};
					return route.fulfill({json:{BackupID:job.BackupID}});
				}
				if (name === "download") job = {State:"working",Action:name,Image:"firmware.zip",Message:"Receiving image",Transfer:true,Bytes:50,Total:100};
				if (name === "install") job = {State:"done",Action:name,Image:"firmware.zip",Slot:args.Slot,Message:"Image installed.",Details:"Extracting the image\nSlot installed with the existing multiboot kernel"};
				if (name === "kernel-decision") job = {State:"done",Action:"install",Slot:1,Message:"Image installed."};
				if (name === "backup") job = {State:"working",Action:name,Slot:args.Slot,Message:"Compressing the filesystem",Details:"Reading the backup source\nCompressing the filesystem"};
				if (name === "rescue-update") job = {State:"done",Action:name,Message:"Internal rescue updated and verified."};
				if (name === "restart") { job = {State:"working",Action:name,Message:"Restarting"}; failStatus = true; }
			}
			return route.fulfill({json:true});
		});
		const visible = async text => p.getByText(text,{exact:true}).first().waitFor();
		await p.goto(origin);
		await visible("Recover your Dreambox.");
		await visible("For reliable multiboot, install OpenATV 8 or newer in slot 1.");
		assert.equal(await p.locator("#device-model").textContent(),model.toUpperCase());
		for (const width of [320,360,390,768,1024,1440]) {
			await p.setViewportSize({width,height:840});
			assert.ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth),`home overflow at ${width}`);
			const [brand,select] = await Promise.all([p.locator(".brand").boundingBox(),p.locator("#appearance").boundingBox()]);
			assert.ok(brand.x+brand.width <= select.x+1,`header overlap at ${width}`);
		}
		await p.setViewportSize({width:360,height:800});
		for (const choice of ["system","light","dark"]) {
			await p.selectOption("#appearance",choice);
			const colors = await p.locator("#appearance").evaluate(el => [getComputedStyle(el).color,getComputedStyle(el).backgroundColor,document.documentElement.dataset.theme]);
			assert.deepEqual(colors,choice === "light"
				? ["rgb(22, 44, 53)","rgb(255, 255, 255)","light"]
				: ["rgb(255, 255, 255)","rgb(24, 56, 61)","dark"]);
		}
		await p.selectOption("#appearance","system");
		await p.emulateMedia({colorScheme:"light"});
		await p.waitForFunction(() => document.documentElement.dataset.theme === "light");
		assert.equal(await p.locator("#appearance").evaluate(e=>getComputedStyle(e).backgroundColor),"rgb(255, 255, 255)");
		await p.locator("#appearance").click(); await p.keyboard.press("Escape");
		await p.selectOption("#appearance","dark");
		await p.reload();
		assert.equal(await p.locator("#appearance").inputValue(),"dark");
		await p.locator("#hardware summary").click(); await visible("BCM72521");
		await p.getByRole("link",{name:"Choose image",exact:true}).click();
		await visible("Choose an installed image.");
		assert.equal(await p.locator('[data-act="start"][data-slot="5"]').isDisabled(),true);
		await p.locator('[data-act="start"][data-slot="1"]').click();
		await p.getByRole("button",{name:"Cancel",exact:true}).click(); assert.equal(actions.length,0);
		await p.goto(origin+"/#maintenance");
		await p.getByRole("button",{name:"Remove multiboot…"}).click();
		await p.keyboard.press("Escape"); assert.equal(actions.length,0);
		await p.getByRole("button",{name:"Restart",exact:true}).click();
		await p.keyboard.press("Escape"); assert.equal(actions.length,0);
		await p.goto(origin+"/#download");
		await p.getByRole("button",{name:"Download image",exact:true}).first().click();
		await visible("50.0%");
		assert.equal(await p.locator("#meter").getAttribute("value"),"50");
		failStatus = true; await visible("Connection lost");
		failStatus = false; await visible("50.0%");
		job = {State:"ready",Action:"download",Image:"firmware.zip",ImageID:"staged-a"};
		await p.getByRole("link",{name:"Choose destination"}).click();
		await visible("For reliable multiboot, install OpenATV 8 or newer in slot 1.");
		await p.locator('[data-act="select"][data-slot="6"]').click();
		await p.getByRole("button",{name:"Review selection"}).click();
		assert.equal(await p.locator("#install").isDisabled(),true);
		await p.locator("#overwrite").check(); await p.locator("#install").click();
		await visible("Image installed successfully");
		await p.reload();
		await p.getByText("Details",{exact:true}).click();
		assert.match(await p.locator("#job-details p").innerText(),/existing multiboot kernel/);
		await p.getByRole("button",{name:"Start new image"}).click();
		await visible("Start Slot 6?"); await p.keyboard.press("Escape");
		assert.deepEqual(actions,[["download",{URL:"https://example.invalid/firmware.zip",Name:"openATV 8.1"}],["install",{Slot:6,Confirm:"install",ImageID:"staged-a"}]]);
		job = {State:"error",Action:"download",Image:"openATV 8.1",DownloadKey:"missing81",DownloadIssue:"Image not available",Message:"The server returned an empty response."};
		await p.reload(); await visible("Image not available");
		await p.getByRole("link",{name:"Choose another download"}).click();
		const missing = p.locator("#download label").filter({hasText:"openATV 8.1"});
		assert.match(await missing.innerText(),/Last attempt: Image not available/);
		assert.doesNotMatch(await p.locator("#download label").filter({hasText:"openATV 8.0"}).innerText(),/Last attempt/);
		await p.goto(origin+"/#job");
		job = {State:"kernel-choice",Action:"install",Slot:1,DecisionID:"decision-for-slot-1",Message:"DreamOS in slot 1 disables multiboot."};
		await p.reload(); await visible("Install slot 1 as a single image?");
		assert.match(await p.locator("main p[role=status]").innerText(),/Slots 2 and higher will no longer boot/);
		await p.getByRole("button",{name:"Disable multiboot and install…"}).click();
		await p.keyboard.press("Escape");
		assert.notEqual(actions.at(-1)[0],"kernel-decision");
		await p.getByRole("button",{name:"Disable multiboot and install…"}).click();
		await p.locator("#dialog-accept").click();
		await visible("Image installed successfully");
		assert.deepEqual(actions.at(-1),["kernel-decision",{DecisionID:"decision-for-slot-1",Confirm:"disable-multiboot"}]);
		job = {State:"error",Action:"install",Slot:1,DestinationAtRisk:true,Message:"Write failed",Details:"Clearing slot 1\nWrite failed"};
		await visible("Flash failed — do not boot this image");
		assert.match(await p.locator(".failed-flash [role=alert]").innerText(),/slot 1.*unable to boot/);
		assert.equal(await p.getByRole("button",{name:"Start new image"}).count(),0);
		await p.getByRole("link",{name:"Upload a replacement image"}).click();
		job = {State:"ready",Action:"upload",Image:"dreambox-image-deb-dm7080-20211029.tar.xz",ImageID:"dreamos-test"};
		await p.goto(origin+"/#target"); await p.locator('[data-act="select"][data-slot="1"]').click();
		await visible("DreamOS in slot 1 disables multiboot.");
		await p.getByRole("button",{name:"Review selection"}).click();
		await visible("DreamOS in slot 1 disables multiboot.");
		await p.goto(origin+"/#maintenance");
		await p.getByRole("link",{name:"Back up an image or settings"}).click();
		await p.locator("#backup-form").waitFor();
		assert.equal(await p.locator("#backup-form option").count(),4);
		await p.selectOption('[name="slot"]',"2");
		await p.getByRole("radio",{name:/Settings only/}).check();
		await p.getByRole("button",{name:"Create backup"}).click(); await p.getByRole("heading",{name:"Compressing the filesystem"}).waitFor();
		await p.getByText("Details",{exact:true}).click();
		assert.match(await p.locator("#job-details p").innerText(),/Reading the backup source/);
		job = {State:"done",Action:"backup",Slot:2,Filename:`${model}-slot2-settings.tar.gz`,BackupID:"backup-abc",Message:"Download the archive."};
		const download = p.getByRole("link",{name:"Download backup"});
		await download.waitFor(); assert.equal(await download.getAttribute("href"),"/api/backup-file?id=backup-abc");
		assert.equal(await p.locator("#job-details").evaluate(e=>e.open),true);
		const [backup,args] = actions.at(-1); assert.equal(backup,"backup"); assert.equal(args.Kind,"settings"); assert.equal(args.Slot,2); assert.match(args.Date,/^\d{4}-\d{2}-\d{2}$/);
		job = {State:"error",Action:"backup",Message:"Insufficient rescue RAM <test>"};
		await visible("Operation failed"); assert.match(await p.locator("#job-details p").innerText(),/Insufficient rescue RAM <test>/);
		await p.goto(origin+"/#maintenance");
		await p.getByRole("link",{name:"Rescue updates"}).click();
		await visible(`No rescue update has been published for the ${model.toUpperCase()} yet.`);
		const update = p.getByRole("button",{name:"Update internal rescue…"});
		assert.equal(await update.count(),0);
		firmware.Release = {Version:"1.0",Bytes:14000000,SHA256:"ab".repeat(32)};
		await p.getByRole("button",{name:"Check again"}).click();
		const before = actions.length;
		await update.click(); await p.getByRole("button",{name:"Cancel",exact:true}).click();
		assert.equal(actions.length,before);
		await update.click(); await p.locator("#dialog-accept").click(); await visible("Internal rescue verified");
		assert.equal(actions.at(-1)[1].ImageID,firmware.Release.SHA256);
		assert.equal(actions.at(-1)[1].Confirm,"rescue-update");
		await p.goto(origin+"/#firmware"); competingUpdate = true;
		await update.click(); await p.locator("#dialog-accept").click();
		await p.getByRole("heading",{name:"Writing internal rescue"}).waitFor();
		assert.equal(await p.getByRole("heading",{name:"Operation failed"}).count(),0);
		competingUpdate = false; job = {State:"done",Action:"rescue-update",Message:"Internal rescue updated and verified."};
		await visible("Internal rescue verified"); firmware.Installed = true;
		await p.goto(origin+"/#firmware"); await visible("This build is already installed in the internal rescue."); assert.equal(await update.count(),0);
		await p.goto(origin+"/#upload");
		await p.locator("#file").setInputFiles({name:"openATV & friends.zip",mimeType:"application/zip",buffer:Buffer.from("fixture")});
		await p.locator("#upload").click();
		await p.getByRole("link",{name:"Choose destination"}).click();
		await visible("openATV & friends.zip");
		layout.Multi = false; layout.Profile = "Single image"; layout.Slots = [slots[0]];
		await p.reload(); await visible("Internal image");
		await p.goto(origin+"/#maintenance");
		assert.equal(await p.locator('[data-act="remove"]').count(),0);
		await p.goto(origin+"/#backup"); assert.match(await p.locator("#backup-form option").innerText(),/Internal image/);
		await p.getByRole("button",{name:"Restart",exact:true}).click();
		await p.locator("#dialog-accept").click(); await visible("Restart requested");
		assert.deepEqual(errors,[]);
		nand = true; failStatus = false; job = {State:"idle"};
		await p.goto(origin+"/#backup"); await p.reload();
		await p.locator('#backup-form input[name="slot"][value="1"]').waitFor({state:"attached"});
		const receiving = p.waitForEvent("download");
		await p.getByRole("button",{name:"Create backup"}).click();
		assert.equal((await receiving).suggestedFilename(),"dm520-test.zip");
		await p.getByRole("heading",{name:"Backup sent"}).waitFor();
		assert.equal(await p.getByRole("link",{name:"Download backup"}).count(),0);
		assert.deepEqual(errors,[]);
		console.log("PASS responsive layout, theme contrast, hardware, navigation, confirmations, upload, OTA progress, reconnection, staged install, persisted slot and single-image mode.");
	} finally { await browser.close(); server.close(); }
}
test().catch(error => { console.error(error); process.exitCode=1; });
