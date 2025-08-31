/// <reference path="../worker-configuration.d.ts" />
import { connect } from "cloudflare:sockets";
export class SQLDriver {
    constructor(ctx, env) {
        this.port = Number(env.GO_PORT);
        this.ctx = ctx;
    }
    async fetch(request) {
        console.log(this.ctx);
        const socket = connect(`localhost:${this.port}`);
        const writer = socket.writable.getWriter();
        const reader = socket.readable.getReader();
        const encoder = new TextEncoder();
        const decoder = new TextDecoder();
        const message = "hello";
        const encoded = encoder.encode(`${message}\n`);
        await writer.write(encoded);
        const { value } = await reader.read();
        const response = decoder.decode(value).trim();
        this.ctx.storage.sql.exec(`
      CREATE TABLE IF NOT EXISTS count (
        id INTEGER PRIMARY KEY
      )
    `);
        this.ctx.storage.sql.exec(`
      INSERT INTO count DEFAULT VALUES
    `);
        const result = this.ctx.storage.sql.exec(`
      SELECT COUNT(*) as count FROM count
    `);
        return new Response(`TCP Response: ${response}, Count: ${result.next().value?.count}`);
    }
}
export default {
    fetch(request, env) {
        return env.SQLDRIVER.get(env.SQLDRIVER.idFromName("test")).fetch(request);
    },
};
