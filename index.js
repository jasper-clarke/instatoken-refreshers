import { TOKENS } from "./tokens.js";
import { writeFile as _writeFile } from "fs/promises";
import { fetchData } from "./utils.js";

const ROOT_URL = "https://graph.instagram.com/refresh_access_token";

async function start() {
  const date = new Date().toLocaleString("tr-TR", {
    day: "numeric",
    month: "long",
    year: "numeric",
    hour: "numeric",
    minute: "numeric",
    hour12: false,
    timeZone: "Europe/London",
  });

  let log = `STARTED: ${date}\n`;

  for (const { token, username } of TOKENS) {
    try {
      const searchParams = new URLSearchParams({
        grant_type: "ig_refresh_token",
        access_token: token,
      });

      const result = await fetchData(`${ROOT_URL}?${searchParams}`);
      console.log(`${username}\n${result}\n`);
      log += `\n${username}`;
    } catch (error) {
      console.error(`Error refreshing token for ${username}:`, error);
      log += `\n${username} - ERROR: ${error.message}`;
    }
  }

  await writeFile(log, "log", "./", "txt");
}

async function writeFile(data, name, dir, ext) {
  const content = ext === "json" ? JSON.stringify(data) : data;
  const filepath = `${dir}${name}.${ext}`;

  await _writeFile(filepath, content);
  console.log(`${filepath} written.`);
}

start().catch(console.error);
