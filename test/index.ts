import { Miniflare, Log, LogLevel } from "miniflare";

const mf = new Miniflare({
	modules: true,
	log: new Log(LogLevel.DEBUG),
	logRequests: true,
	scriptPath: "worker.js",
	durableObjectsPersist: true,
	durableObjects: {
		// Note Object1 is exported from main (string) script
		SQLDRIVER: {
			className: "SQLDriver",
			useSQLite: true,
		},
	},
	bindings: {
		GO_PORT: Deno.env.get("GO_PORT") ?? "8788",
	},
});
// await mf.dispose();
Deno.serve(
	{ port: Number.parseInt(Deno.env.get("PORT") ?? "8787") },
	async (req) => {
		const res = await mf.dispatchFetch(req.url, req);
		return new Response(res.body, { status: res.status, headers: res.headers });
	},
);
