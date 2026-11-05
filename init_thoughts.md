read through lilya_idea.txt. It is a very loose idea about a CRM fitness system. the main idea is that there is an admin side which uploads videos and user who watch it and interact with it. Right now there is very little business logic so need to leave a lot of space for further
  bundeling of videos. But for now: video upload is a post where additional pictures or text can be added. User see it in their side with
  abilitites to view, mark it as done (thats it for now). The important part is video's analysis: what poses, which body have been targeted
  (like lilya described). this analysis is displayed in the post and then can be searched from the main page of user.
Those interactions can happen from telegram bot, webview or mobile. Right now lets contcentrate on webview but leave space for other frontends

For now lets upload 1 video and serve it from the server with additional S3 (or maybe use existing video hosting)  in the future. Use whatever language is best, make a plan and implement it. As a general rule for this implementation is that I will deploy it so make login email/password based and treat this not as a localc prototype
