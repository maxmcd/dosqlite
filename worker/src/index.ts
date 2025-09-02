/// <reference path="../worker-configuration.d.ts" />
import { connect } from "cloudflare:sockets";

interface ExecRequest {
	cmd: "exec";
	sql: string;
	params: any[];
}

interface ExecResponse {
	ok: boolean;
	error?: string;
}

interface QueryRequest {
	cmd: "query";
	sql: string;
	params: any[];
}

interface QueryResponse {
	ok: boolean;
	rows?: Record<string, any>[];
	error?: string;
}

interface BlobValue {
	type: "blob";
	data: string; // base64 encoded
}

class ProtocolHandler {
	private buffer: Uint8Array = new Uint8Array(0);

	addData(data: Uint8Array): void {
		const newBuffer = new Uint8Array(this.buffer.length + data.length);
		newBuffer.set(this.buffer);
		newBuffer.set(data, this.buffer.length);
		this.buffer = newBuffer;
	}

	readFrame(): Uint8Array | null {
		if (this.buffer.length < 4) {
			return null; // Need more data
		}

		// Read 4-byte big-endian length
		const view = new DataView(
			this.buffer.buffer,
			this.buffer.byteOffset,
			this.buffer.byteLength,
		);
		const frameLength = view.getUint32(0, false); // false = big-endian

		if (this.buffer.length < 4 + frameLength) {
			return null; // Need more data
		}

		const frame = this.buffer.slice(4, 4 + frameLength);
		this.buffer = this.buffer.slice(4 + frameLength);
		return frame;
	}

	writeFrame(data: any): Uint8Array {
		const jsonStr = JSON.stringify(data);
		const payload = new TextEncoder().encode(jsonStr);

		// Create frame with 4-byte big-endian length prefix
		const frame = new Uint8Array(4 + payload.length);
		const view = new DataView(frame.buffer);
		view.setUint32(0, payload.length, false); // false = big-endian
		frame.set(payload, 4);

		return frame;
	}
}

export class SQLDriver {
	ctx: DurableObjectState;
	port: number;
	tcpSocket?: Socket;
	protocolHandler: ProtocolHandler;

	constructor(ctx: DurableObjectState, env: { GO_PORT: string }) {
		this.port = Number(env.GO_PORT);
		this.ctx = ctx;
		this.protocolHandler = new ProtocolHandler();
	}

	async ensureTcpConnection() {
		if (!this.tcpSocket) {
			await this.initTcpConnection();
		}
	}

	async initTcpConnection() {
		console.log("Initializing TCP connection to", `localhost:${this.port}`);
		this.tcpSocket = connect(`localhost:${this.port}`);


		this.tcpSocket.closed.then(() => {
			console.log("TCP connection closed");
			this.tcpSocket = undefined;
		}).catch((err) => {
			console.error("TCP connection error:", this.serializeError(err));
		});

		await this.tcpSocket.opened;
		console.log("TCP connection established");
		this.startProtocolHandler();
	}

	async startProtocolHandler() {
		try {
			const reader = this.tcpSocket!.readable.getReader();
			const writer = this.tcpSocket!.writable.getWriter();

			while (this.tcpSocket) {
				const { value, done } = await reader.read();
				if (done) break;

				this.protocolHandler.addData(value);

				// Process all complete frames
				let frame: Uint8Array | null;
				while ((frame = this.protocolHandler.readFrame()) !== null) {
					const response = await this.handleCommand(frame);
					await writer.write(response);
				}
			}
		} catch (err) {
			console.error("Protocol handler error:", this.serializeError(err));
		}
	}

	async handleCommand(frame: Uint8Array): Promise<Uint8Array> {
		try {
			// Parse JSON command
			const jsonStr = new TextDecoder().decode(frame);
			const command = JSON.parse(jsonStr);

			switch (command.cmd) {
				case "exec":
					return this.handleExec(command as ExecRequest);
				case "query":
					return this.handleQuery(command as QueryRequest);
				default:
					return this.protocolHandler.writeFrame({
						ok: false,
						error: `Unknown command: ${command.cmd}`,
					});
			}
		} catch (error) {
			return this.protocolHandler.writeFrame({
				ok: false,
				error: this.serializeError(error),
			});
		}
	}

	async handleExec(req: ExecRequest): Promise<Uint8Array> {
		try {
			// Convert parameters
			const params = req.params.map((p) => this.convertFromJSONValue(p));

			// Execute SQL
			this.ctx.storage.sql.exec(req.sql, ...params);

			const response: ExecResponse = {
				ok: true,
			};

			return this.protocolHandler.writeFrame(response);
		} catch (error) {
			const response: ExecResponse = {
				ok: false,
				error: this.serializeError(error),
			};
			return this.protocolHandler.writeFrame(response);
		}
	}

	async handleQuery(req: QueryRequest): Promise<Uint8Array> {
		try {
			// Convert parameters
			const params = req.params.map((p) => this.convertFromJSONValue(p));

			// Execute query
			const cursor = this.ctx.storage.sql.exec(req.sql, ...params);

			// Convert rows to JSON-safe format using cursor.toArray()
			const rows = cursor.toArray().map((row) => {
				const convertedRow: Record<string, any> = {};
				for (const [key, value] of Object.entries(row)) {
					convertedRow[key] = this.convertToJSONValue(value);
				}
				return convertedRow;
			});

			const response: QueryResponse = {
				ok: true,
				rows: rows,
			};

			return this.protocolHandler.writeFrame(response);
		} catch (error) {
			const response: QueryResponse = {
				ok: false,
				error: this.serializeError(error),
			};
			return this.protocolHandler.writeFrame(response);
		}
	}

	private convertFromJSONValue(jsonValue: any): any {
		// Handle blob values
		if (typeof jsonValue === "object" && jsonValue?.type === "blob") {
			// Decode base64 to Uint8Array for SQLite
			return Uint8Array.from(
				atob(jsonValue.data),
				(c) => c.charCodeAt(0),
			);
		}
		return jsonValue; // Pass through other values
	}

	private convertToJSONValue(sqliteValue: any): any {
		// Handle binary data from SQLite
		if (sqliteValue instanceof ArrayBuffer) {
			const uint8Array = new Uint8Array(sqliteValue);
			const base64 = btoa(String.fromCharCode(...uint8Array));
			return { type: "blob", data: base64 };
		}
		if (sqliteValue instanceof Uint8Array) {
			const base64 = btoa(String.fromCharCode(...sqliteValue));
			return { type: "blob", data: base64 };
		}
		return sqliteValue; // Pass through numbers, strings, null, boolean
	}

	async fetch(request: Request): Promise<Response> {
		await this.ensureTcpConnection();
		return new Response("DoSQLite TCP Bridge Ready");
	}

	private serializeError(err: unknown): string {
		if (err instanceof Error) {
			return `${err.name}: ${err.message}${
				err.stack ? "\n" + err.stack : ""
			}`;
		}
		return String(err);
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


