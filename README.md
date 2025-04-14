# VideoSync
Synchronized video streaming service written in Go with a web-frontend.

## Overview
The frontend has two main pages: one for creating/joining sessions and another for the video player. The backend is written in Go and uses Redis for state management. They mentioned WebSocket and REST APIs.

First, when a user creates a session, the frontend sends a POST request to /api/sessions. The backend generates a session key using UUID and stores it in Redis. The frontend then displays this key. That's straightforward.

When joining a session, the frontend sends a GET request to /api/sessions/{key}/validate to check if the session exists. The backend checks Redis and responds with a boolean. If valid, the user is redirected to the player page with the session key as a query parameter.

Now, the video player page establishes a WebSocket connection to the backend. The WebSocket URL includes the session key. The backend validates this key again when the WebSocket connection is initiated. The first user to connect becomes the host, and subsequent users are participants.

The host can control playback (play, pause, seek). These actions send WebSocket messages to the backend. The backend updates the session state in Redis and broadcasts the new state to all connected clients in the same session. Participants receive these state updates via WebSocket and adjust their video players accordingly.

For video streaming, the frontend requests the video file via /api/video. The backend serves the video using http.ServeContent, which handles range requests for efficient streaming. The frontend uses the HTML5 video element to play the video, and HLS.js if needed for adaptive streaming, though the current backend serves a local file directly.

There's also a heartbeat mechanism over WebSocket to ensure connections are alive. If a client disconnects, the backend handles it, and if the host disconnects, the session might need a new host, but the current code only removes the host key in Redis.