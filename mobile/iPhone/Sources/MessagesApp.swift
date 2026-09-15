import SwiftUI

/// Messages — an iPhone client for the Go messageServer in this repo.
///
/// The app is a thin front end over the server's JSON API. Each install names
/// itself (People ▸ Your username) and adds the people it wants to talk to, and
/// any number of installs pointed at the same server can then chat.
@main
struct MessagesApp: App {
    @StateObject private var client = ChatClient()

    var body: some Scene {
        WindowGroup {
            ChatView()
                .environmentObject(client)
        }
    }
}
