# instatokend

Pros:

- Open Source
- Entirely cross-platform binary
- Can be deployed to any hosting platform

Cons:

- Needs to be run on a server with read/write access to the filesystem

The configuration file must be named `config.json` and be placed in the same directory as the executable.

Example configuration file:

```json
{
  "port": "8080",
  "refresh_freq": "daily", // daily, weekly, monthly, test(every 5 minutes)
  "my-instagram-1": {
    "token": "mysecrettoken"
  },
  "my-instagram-2": {
    "token": "another-secret-token"
  }
}
```

Below is a minimal example of using [Instafeed.js](https://instafeedjs.com) with the `instatokend` daemon:
As you can see you can perform a GET request to the `/token/{account}` endpoint to get the access token for a specific account.

```html
<!-- Include Instafeed.js here -->

<div id="instafeed"></div>

<script type="text/javascript">
  async function getInstagramToken() {
    try {
      const response = await fetch(
        // Your instatokend server URL
        "http://123.456.789.100:8080/token/my-instagram-1",
      );
      const data = await response.json();
      return data.token;
    } catch (error) {
      console.error("Error fetching token:", error);
      throw error;
    }
  }

  getInstagramToken()
    .then((token) => {
      const feed = new Instafeed({
        accessToken: token,
      });
      feed.run();
    })
    .catch((error) => {
      console.error("Error setting up Instagram feed:", error);
    });
</script>
```
