async function fetchData(url) {
  const init = {
    headers: {
      "content-type": "text/html;charset=UTF-8",
      "User-Agent":
        "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:52.0) Gecko/20100101 Firefox/52.0",
    },
  };

  const response = await fetch(url, init);
  if (!response.ok) {
    throw new Error(`HTTP error! status: ${response.status}`);
  }
  return await response.text();
}

export { fetchData };
