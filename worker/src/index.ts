/// <reference path="../worker-configuration.d.ts" />
import { connect } from "cloudflare:sockets";

export class SQLDriver {
	ctx: DurableObjectState;
	port: number;
	tcpSocket?: Socket;
	constructor(ctx: DurableObjectState, env: { GO_PORT: string }) {
		this.port = Number(env.GO_PORT);
		this.ctx = ctx;
	}
	async ensureTcpConnection() {
		if (!this.tcpSocket) {
			await this.initTcpConnection();
		}
	}

	async initTcpConnection() {
		this.tcpSocket = connect(`localhost:${this.port}`)
		this.tcpSocket.closed.then(() => {
			console.log('tcpSocket.closed');
			this.tcpSocket = undefined;
		}).catch((err) => {
			console.error('tcpSocket.closed error:', serializeError(err));
		})	;

		console.log(await this.tcpSocket.opened);
		console.log(`TCP connection established`);
		this.tcpEchoLoop();
	}

	async tcpEchoLoop() {
		try {
			while (this.tcpSocket) {
				const reader = this.tcpSocket.readable.getReader();
				const writer = this.tcpSocket.writable.getWriter();
				const { value, done } = await reader.read();
				if (done) {
					console.log('TCP connection closed');
					break;
				}

				const message = new TextDecoder().decode(value).trim();
				console.log('Received TCP message:', message);

				// Echo back with JS suffix
				const response = `${message} echoed from JS\n`;
				await writer.write(new TextEncoder().encode(response));
			}
		} catch (err) {
			console.error('TCP echo loop error:', err);
		}
	}

	async fetch(request: Request): Promise<Response> {
		await this.ensureTcpConnection()

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
		return new Response(
			`TCP Response: Count: ${result.next().value?.count}`,
		);
	}
}

export default {
	fetch(
		request: Request,
		env: { SQLDRIVER: DurableObjectNamespace },
	): Promise<Response> {
		return env.SQLDRIVER.get(env.SQLDRIVER.idFromName("test")).fetch(request);
	},
};


function serializeError(err: unknown) {
	if (err instanceof Error) {
		if (err.stack && err.stack.includes(err.message)) {
			return err.stack;
		}
		return `${err.name}: ${err.message}\n${err.stack}`;
	}
	return String(err);
}
